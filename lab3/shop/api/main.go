package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const schema = `CREATE TABLE IF NOT EXISTS orders (
	id           BIGSERIAL PRIMARY KEY,
	item         TEXT        NOT NULL,
	qty          INT         NOT NULL DEFAULT 1,
	status       TEXT        NOT NULL DEFAULT 'new',
	created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
	processed_at TIMESTAMPTZ
)`

var (
	httpRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "HTTP requests by handler, method and status code.",
	}, []string{"handler", "method", "code"})
	httpDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "HTTP request latency.",
		Buckets: prometheus.DefBuckets,
	}, []string{"handler"})
	dbUp = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "api_db_ready",
		Help: "1 if the database is reachable and the schema is in place.",
	})
)

type order struct {
	ID          int64      `json:"id"`
	Item        string     `json:"item"`
	Qty         int        `json:"qty"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	ProcessedAt *time.Time `json:"processed_at,omitempty"`
}

type server struct {
	pool       *pgxpool.Pool
	dbReady    atomic.Bool
	healthFail bool
	version    string
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	healthFail, _ := strconv.ParseBool(os.Getenv("HEALTH_FAIL"))
	s := &server{healthFail: healthFail, version: env("APP_VERSION", "dev")}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// pgxpool.New does not connect eagerly, so the API starts (and /health
	// answers) even when Postgres does not exist yet or is down.
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		pool, err := pgxpool.New(ctx, dsn)
		if err != nil {
			log.Fatalf("bad DATABASE_URL: %v", err)
		}
		defer pool.Close()
		s.pool = pool
		go s.ensureSchema(ctx)
	} else {
		log.Print("DATABASE_URL is empty, /order and /orders will return 503")
	}

	mux := http.NewServeMux()
	mux.Handle("GET /health", instrument("health", s.health))
	mux.Handle("GET /version", instrument("version", s.versionHandler))
	mux.Handle("POST /order", instrument("order", s.createOrder))
	mux.Handle("GET /orders", instrument("orders", s.listOrders))
	mux.Handle("GET /metrics", promhttp.Handler())

	srv := &http.Server{Addr: ":" + env("PORT", "8080"), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Printf("api %s listening on %s (HEALTH_FAIL=%v)", s.version, srv.Addr, s.healthFail)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	<-ctx.Done()
	log.Print("SIGTERM received, draining in-flight requests")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

// ensureSchema retries until the database is reachable, then keeps a
// lightweight ping loop so api_db_ready reflects reality.
func (s *server) ensureSchema(ctx context.Context) {
	for {
		c, cancel := context.WithTimeout(ctx, 3*time.Second)
		_, err := s.pool.Exec(c, schema)
		cancel()
		if err == nil {
			if !s.dbReady.Swap(true) {
				log.Print("database ready")
			}
			dbUp.Set(1)
		} else {
			if s.dbReady.Swap(false) {
				log.Printf("database lost: %v", err)
			}
			dbUp.Set(0)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func (s *server) health(w http.ResponseWriter, _ *http.Request) {
	if s.healthFail {
		http.Error(w, "unhealthy (HEALTH_FAIL=true)", http.StatusInternalServerError)
		return
	}
	w.Write([]byte("ok\n"))
}

func (s *server) versionHandler(w http.ResponseWriter, _ *http.Request) {
	host, _ := os.Hostname()
	writeJSON(w, http.StatusOK, map[string]string{"version": s.version, "pod": host})
}

func (s *server) dbAvailable(w http.ResponseWriter) bool {
	if s.pool == nil || !s.dbReady.Load() {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return false
	}
	return true
}

func (s *server) createOrder(w http.ResponseWriter, r *http.Request) {
	if !s.dbAvailable(w) {
		return
	}
	in := struct {
		Item string `json:"item"`
		Qty  int    `json:"qty"`
	}{Item: "widget", Qty: 1}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	if in.Item == "" || in.Qty < 1 {
		http.Error(w, "item must be non-empty and qty >= 1", http.StatusBadRequest)
		return
	}

	var o order
	err := s.pool.QueryRow(r.Context(),
		`INSERT INTO orders (item, qty) VALUES ($1, $2)
		 RETURNING id, item, qty, status, created_at, processed_at`,
		in.Item, in.Qty,
	).Scan(&o.ID, &o.Item, &o.Qty, &o.Status, &o.CreatedAt, &o.ProcessedAt)
	if err != nil {
		http.Error(w, "insert failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, o)
}

func (s *server) listOrders(w http.ResponseWriter, r *http.Request) {
	if !s.dbAvailable(w) {
		return
	}
	limit := 50
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 1000 {
		limit = v
	}
	rows, err := s.pool.Query(r.Context(),
		`SELECT id, item, qty, status, created_at, processed_at
		 FROM orders ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		http.Error(w, "query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	orders := []order{}
	for rows.Next() {
		var o order
		if err := rows.Scan(&o.ID, &o.Item, &o.Qty, &o.Status, &o.CreatedAt, &o.ProcessedAt); err != nil {
			http.Error(w, "scan failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		orders = append(orders, o)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "rows failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, orders)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.code = code
	r.ResponseWriter.WriteHeader(code)
}

func instrument(name string, h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		h(rec, r)
		httpDuration.WithLabelValues(name).Observe(time.Since(start).Seconds())
		httpRequests.WithLabelValues(name, r.Method, strconv.Itoa(rec.code)).Inc()
	})
}
