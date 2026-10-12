package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Same DDL as api: whichever starts first creates the table.
const schema = `CREATE TABLE IF NOT EXISTS orders (
	id           BIGSERIAL PRIMARY KEY,
	item         TEXT        NOT NULL,
	qty          INT         NOT NULL DEFAULT 1,
	status       TEXT        NOT NULL DEFAULT 'new',
	created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
	processed_at TIMESTAMPTZ
)`

// SKIP LOCKED lets several worker replicas poll concurrently without
// grabbing the same rows.
const claim = `UPDATE orders SET status = 'processed', processed_at = now()
WHERE id IN (
	SELECT id FROM orders WHERE status = 'new'
	ORDER BY id LIMIT $1
	FOR UPDATE SKIP LOCKED
)
RETURNING id`

var (
	processed = promauto.NewCounter(prometheus.CounterOpts{
		Name: "worker_orders_processed_total",
		Help: "Orders marked as processed.",
	})
	pollErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "worker_poll_errors_total",
		Help: "Failed polling iterations (DB unreachable, query error).",
	})
	lastSuccess = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "worker_last_success_timestamp_seconds",
		Help: "Unix time of the last successful poll.",
	})
	backlog = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "worker_orders_backlog",
		Help: "Orders with status 'new' seen at the last poll.",
	})
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	healthFail, _ := strconv.ParseBool(os.Getenv("HEALTH_FAIL"))
	interval, err := time.ParseDuration(env("POLL_INTERVAL", "2s"))
	if err != nil {
		log.Fatalf("bad POLL_INTERVAL: %v", err)
	}
	batch, err := strconv.Atoi(env("BATCH_SIZE", "10"))
	if err != nil || batch < 1 {
		log.Fatalf("bad BATCH_SIZE: %q", os.Getenv("BATCH_SIZE"))
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		if healthFail {
			http.Error(w, "unhealthy (HEALTH_FAIL=true)", http.StatusInternalServerError)
			return
		}
		w.Write([]byte("ok\n"))
	})
	mux.Handle("GET /metrics", promhttp.Handler())
	srv := &http.Server{Addr: ":" + env("PORT", "8080"), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Printf("worker http on %s, poll every %s, batch %d", srv.Addr, interval, batch)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Print("DATABASE_URL is empty, worker idles (only /health and /metrics)")
	} else {
		pool, err := pgxpool.New(ctx, dsn)
		if err != nil {
			log.Fatalf("bad DATABASE_URL: %v", err)
		}
		defer pool.Close()
		go loop(ctx, pool, interval, batch)
	}

	<-ctx.Done()
	log.Print("SIGTERM received, stopping")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(shutdownCtx)
}

func loop(ctx context.Context, pool *pgxpool.Pool, interval time.Duration, batch int) {
	schemaOK := false
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := poll(ctx, pool, batch, &schemaOK); err != nil {
			pollErrors.Inc()
			log.Printf("poll failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func poll(ctx context.Context, pool *pgxpool.Pool, batch int, schemaOK *bool) error {
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if !*schemaOK {
		if _, err := pool.Exec(c, schema); err != nil {
			return err
		}
		*schemaOK = true
		log.Print("database ready")
	}

	rows, err := pool.Query(c, claim, batch)
	if err != nil {
		return err
	}
	n := 0
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		n++
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if n > 0 {
		processed.Add(float64(n))
		log.Printf("processed %d orders", n)
	}

	var pending int64
	if err := pool.QueryRow(c, `SELECT count(*) FROM orders WHERE status = 'new'`).Scan(&pending); err != nil {
		return err
	}
	backlog.Set(float64(pending))
	lastSuccess.SetToCurrentTime()
	return nil
}
