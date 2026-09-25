package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

var (
	port   = getenv("PORT", "8080")
	tracer = otel.Tracer("api")
	logger = slog.New(traceHandler{slog.NewJSONHandler(os.Stdout, nil)}).With("service", "api")
	client = &http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport), Timeout: 15 * time.Second}
)

// ---------- Метрики (RED) ----------
var (
	requestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total HTTP requests.",
	}, []string{"method", "endpoint", "code"})

	errorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_errors_total",
		Help: "Total HTTP 5xx responses.",
	}, []string{"method", "endpoint"})

	requestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "HTTP request latency.",
		Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2, 3, 5, 10},
	}, []string{"method", "endpoint"})
)

// ---------- Логи: JSON в stdout + trace_id/span_id из контекста ----------
type traceHandler struct{ slog.Handler }

func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return h.Handler.Handle(ctx, r)
}

func (h traceHandler) WithAttrs(a []slog.Attr) slog.Handler {
	return traceHandler{h.Handler.WithAttrs(a)}
}

func (h traceHandler) WithGroup(n string) slog.Handler {
	return traceHandler{h.Handler.WithGroup(n)}
}

// ---------- Трейсы ----------
// Экспортёр включается только если задан OTEL_EXPORTER_OTLP_ENDPOINT (например http://<jaeger>:4318);
// SDK сам допишет /v1/traces. Имя сервиса можно переопределить через OTEL_SERVICE_NAME.
func initTracing(ctx context.Context) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	res, err := resource.New(ctx,
		resource.WithAttributes(attribute.String("service.name", "api")),
		resource.WithFromEnv(),
	)
	if err != nil {
		return nil, err
	}

	opts := []sdktrace.TracerProviderOption{sdktrace.WithResource(res)}
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" {
		exp, err := otlptracehttp.New(ctx)
		if err != nil {
			return nil, err
		}
		opts = append(opts, sdktrace.WithBatcher(exp))
	}

	tp := sdktrace.NewTracerProvider(opts...)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

// ---------- Middleware: метрики + access-лог; снаружи — корневой спан otelhttp ----------
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func instrument(endpoint string, h http.HandlerFunc) http.Handler {
	observed := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		h(rec, r)
		dur := time.Since(start)

		code := rec.status
		requestsTotal.WithLabelValues(r.Method, endpoint, strconv.Itoa(code)).Inc()
		requestDuration.WithLabelValues(r.Method, endpoint).Observe(dur.Seconds())

		level := slog.LevelInfo
		if code >= 500 {
			errorsTotal.WithLabelValues(r.Method, endpoint).Inc()
			level = slog.LevelError
		}
		logger.Log(r.Context(), level, "request handled",
			"method", r.Method,
			"path", r.URL.Path,
			"status", code,
			"duration_ms", float64(dur.Microseconds())/1000,
		)
	})
	return otelhttp.NewHandler(observed, "GET "+endpoint)
}

// ---------- Хендлеры ----------
func health(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("ok"))
}

func fail(w http.ResponseWriter, r *http.Request) {
	err := errors.New("simulated failure")
	span := trace.SpanFromContext(r.Context())
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
	logger.ErrorContext(r.Context(), "simulated failure in /fail", "error", err.Error())
	http.Error(w, "internal error", http.StatusInternalServerError)
}

func slow(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "slow-op")
	delay := time.Duration(1000+rand.IntN(2000)) * time.Millisecond
	span.SetAttributes(attribute.Float64("slow.delay_seconds", delay.Seconds()))
	select {
	case <-time.After(delay):
	case <-ctx.Done():
	}
	span.End()

	logger.InfoContext(r.Context(), "slow operation finished", "delay_s", delay.Seconds())
	fmt.Fprintf(w, "slept %.2fs\n", delay.Seconds())
}

// /load?n=200&target=/fail — пачка запросов к самому себе (10 параллельно)
func load(w http.ResponseWriter, r *http.Request) {
	n := 100
	if v, err := strconv.Atoi(r.URL.Query().Get("n")); err == nil && v > 0 {
		n = min(v, 1000)
	}
	target := r.URL.Query().Get("target")
	if !strings.HasPrefix(target, "/") {
		target = "/health"
	}
	url := "http://127.0.0.1:" + port + target

	var ok atomic.Int64
	var wg sync.WaitGroup
	sem := make(chan struct{}, 10)
	for range n {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, url, nil)
			resp, err := client.Do(req)
			if err != nil {
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode < 300 {
				ok.Add(1)
			}
		}()
	}
	wg.Wait()

	logger.InfoContext(r.Context(), "load burst finished",
		"n", n,
		"target", target,
		"ok", ok.Load(),
	)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"sent":   n,
		"target": target,
		"ok":     ok.Load(),
	})
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	shutdownTracing, err := initTracing(ctx)
	if err != nil {
		logger.Error("tracing init failed", "error", err.Error())
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /health", instrument("/health", health))
	mux.Handle("GET /fail", instrument("/fail", fail))
	mux.Handle("GET /slow", instrument("/slow", slow))
	mux.Handle("GET /load", instrument("/load", load))
	mux.Handle("GET /metrics", promhttp.Handler())

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		logger.Info("listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "error", err.Error())
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	shCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shCtx)
	_ = shutdownTracing(shCtx) // дослать буферизованные спаны
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
