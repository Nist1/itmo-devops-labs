// gate — автопилот канареечного релиза api (Лаба 6).
//
// Раз в INTERVAL читает из Prometheus метрики mesh (inbound-прокси Linkerd на подах v2):
// долю ошибок, p95 задержки и RPS. По простому правилу меняет веса v1/v2 в HTTPRoute:
//
//   - на v2 почти нет трафика (RPS < MIN_RPS)           → ждём, ничего не меняем;
//   - старт релиза: не прошло START_DELAY + WARMUP      → ждём: в окне ещё поды прошлого релиза v2;
//   - ошибки > MAX_ERROR_RATE или p95 > MAX_P95_MS      → откат: v2 = 0%, гейт замирает;
//   - после смены веса не прошло WARMUP                 → только следим за откатом, не повышаем;
//   - здорова HEALTHY_CHECKS проверок подряд            → следующий шаг из STEPS;
//   - дошли до 100%                                     → релиз завершён, гейт замирает.
//
// Веса меняются JSON Patch'ем HTTPRoute через API Kubernetes (токен ServiceAccount пода).
// Состояние отдаётся в /metrics, чтобы рисовать в Grafana долю v2 и решения гейта.
//
// Только стандартная библиотека.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	stateProgressing = 0
	statePromoted    = 1
	stateRolledBack  = 2
)

type config struct {
	promURL       string
	namespace     string
	route         string
	v1Backend     string
	v2Backend     string
	interval      time.Duration
	warmup        time.Duration
	startDelay    time.Duration
	steps         []int
	maxErrorRate  float64
	maxP95ms      float64
	minRPS        float64
	healthyChecks int
	qErrorRate    string
	qP95          string
	qRPS          string
	port          string
}

type status struct {
	mu        sync.Mutex
	weight    int
	state     int
	errorRate float64
	p95ms     float64
	rps       float64
	decisions map[string]uint64
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func mustFloat(key, def string) float64 {
	v, err := strconv.ParseFloat(env(key, def), 64)
	if err != nil {
		log.Fatalf("bad %s: %v", key, err)
	}
	return v
}

func loadConfig() config {
	c := config{
		promURL:      env("PROM_URL", "http://kps-kube-prometheus-stack-prometheus.monitoring:9090"),
		namespace:    env("NAMESPACE", "shop"),
		route:        env("ROUTE_NAME", "shop-api"),
		v1Backend:    env("V1_BACKEND", "shop-api-v1"),
		v2Backend:    env("V2_BACKEND", "shop-api-v2"),
		maxErrorRate: mustFloat("MAX_ERROR_RATE", "0.05"),
		maxP95ms:     mustFloat("MAX_P95_MS", "500"),
		minRPS:       mustFloat("MIN_RPS", "0.5"),
		qErrorRate:   os.Getenv("QUERY_ERROR_RATE"),
		qP95:         os.Getenv("QUERY_P95_MS"),
		qRPS:         os.Getenv("QUERY_RPS"),
		port:         env("PORT", "8080"),
	}
	var err error
	if c.interval, err = time.ParseDuration(env("INTERVAL", "15s")); err != nil {
		log.Fatalf("bad INTERVAL: %v", err)
	}
	// Прогрев = окну rate(): повышаем только по данным, целиком снятым ПОСЛЕ смены веса
	if c.warmup, err = time.ParseDuration(env("WARMUP", "1m")); err != nil {
		log.Fatalf("bad WARMUP: %v", err)
	}
	// На старте релиза в окне rate() ещё есть ответы старых подов v2 (прошлый релиз
	// доживает, пока выкатывается новый). START_DELAY — запас на выкатку нового пода
	if c.startDelay, err = time.ParseDuration(env("START_DELAY", "30s")); err != nil {
		log.Fatalf("bad START_DELAY: %v", err)
	}
	if c.healthyChecks, err = strconv.Atoi(env("HEALTHY_CHECKS", "2")); err != nil || c.healthyChecks < 1 {
		log.Fatalf("bad HEALTHY_CHECKS")
	}
	for _, s := range strings.Split(env("STEPS", "10,25,50,75,100"), ",") {
		w, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil || w < 0 || w > 100 {
			log.Fatalf("bad STEPS: %q", s)
		}
		c.steps = append(c.steps, w)
	}
	if c.qErrorRate == "" || c.qP95 == "" || c.qRPS == "" {
		log.Fatal("QUERY_ERROR_RATE, QUERY_P95_MS and QUERY_RPS are required")
	}
	return c
}

// ---------- Prometheus ----------

var errNoData = errors.New("no data")

func promQuery(ctx context.Context, base, q string) (float64, error) {
	u := base + "/api/v1/query?query=" + url.QueryEscape(q)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var out struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			Result []struct {
				Value [2]any `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, err
	}
	if out.Status != "success" {
		return 0, fmt.Errorf("prometheus: %s", out.Error)
	}
	if len(out.Data.Result) == 0 {
		return 0, errNoData
	}
	s, _ := out.Data.Result[0].Value[1].(string)
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) {
		return 0, errNoData
	}
	return v, nil
}

// ---------- Kubernetes ----------

type kube struct {
	client *http.Client
	host   string
	token  string
}

func newKube() (*kube, error) {
	const sa = "/var/run/secrets/kubernetes.io/serviceaccount/"
	token, err := os.ReadFile(sa + "token")
	if err != nil {
		return nil, err
	}
	ca, err := os.ReadFile(sa + "ca.crt")
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca)
	return &kube{
		client: &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}},
		host:   "https://" + os.Getenv("KUBERNETES_SERVICE_HOST") + ":" + os.Getenv("KUBERNETES_SERVICE_PORT"),
		token:  strings.TrimSpace(string(token)),
	}, nil
}

// setWeights меняет веса двух backendRefs первого правила HTTPRoute.
// Операции test страхуют от патча не того бэкенда, если порядок в чарте поменяется.
func (k *kube) setWeights(ctx context.Context, c config, v2 int) error {
	patch := []map[string]any{
		{"op": "test", "path": "/spec/rules/0/backendRefs/0/name", "value": c.v1Backend},
		{"op": "test", "path": "/spec/rules/0/backendRefs/1/name", "value": c.v2Backend},
		{"op": "replace", "path": "/spec/rules/0/backendRefs/0/weight", "value": 100 - v2},
		{"op": "replace", "path": "/spec/rules/0/backendRefs/1/weight", "value": v2},
	}
	body, _ := json.Marshal(patch)
	u := fmt.Sprintf("%s/apis/gateway.networking.k8s.io/v1/namespaces/%s/httproutes/%s", k.host, c.namespace, c.route)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPatch, u, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+k.token)
	req.Header.Set("Content-Type", "application/json-patch+json")
	resp, err := k.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("patch httproute: %s: %s", resp.Status, b)
	}
	return nil
}

// ---------- метрики гейта ----------

func (s *status) write(w io.Writer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprintln(w, "# HELP gate_v2_weight Current traffic weight of v2, percent.")
	fmt.Fprintln(w, "# TYPE gate_v2_weight gauge")
	fmt.Fprintf(w, "gate_v2_weight %d\n", s.weight)
	fmt.Fprintln(w, "# HELP gate_state 0 = progressing, 1 = promoted to 100%, 2 = rolled back.")
	fmt.Fprintln(w, "# TYPE gate_state gauge")
	fmt.Fprintf(w, "gate_state %d\n", s.state)
	fmt.Fprintln(w, "# HELP gate_v2_error_rate Last observed v2 error rate (mesh inbound).")
	fmt.Fprintln(w, "# TYPE gate_v2_error_rate gauge")
	fmt.Fprintf(w, "gate_v2_error_rate %g\n", s.errorRate)
	fmt.Fprintln(w, "# HELP gate_v2_p95_ms Last observed v2 p95 latency, ms (mesh inbound).")
	fmt.Fprintln(w, "# TYPE gate_v2_p95_ms gauge")
	fmt.Fprintf(w, "gate_v2_p95_ms %g\n", s.p95ms)
	fmt.Fprintln(w, "# HELP gate_v2_rps Last observed v2 request rate.")
	fmt.Fprintln(w, "# TYPE gate_v2_rps gauge")
	fmt.Fprintf(w, "gate_v2_rps %g\n", s.rps)
	fmt.Fprintln(w, "# HELP gate_decisions_total Gate decisions by type.")
	fmt.Fprintln(w, "# TYPE gate_decisions_total counter")
	for _, d := range []string{"wait", "hold", "promote", "rollback", "done", "error"} {
		fmt.Fprintf(w, "gate_decisions_total{decision=%q} %d\n", d, s.decisions[d])
	}
}

func (s *status) decide(d string) {
	s.mu.Lock()
	s.decisions[d]++
	s.mu.Unlock()
}

// ---------- главный цикл ----------

func main() {
	c := loadConfig()
	k, err := newKube()
	if err != nil {
		log.Fatalf("kubernetes client: %v", err)
	}
	st := &status{decisions: map[string]uint64{}}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		st.write(w)
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok") })
	srv := &http.Server{Addr: ":" + c.port, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// Каждый запуск гейта — новый релиз: начинаем с первого шага (обычно 10%).
	step := 0
	for {
		if err := k.setWeights(ctx, c, c.steps[0]); err == nil {
			break
		} else {
			log.Printf("initial patch failed, retrying: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
	st.mu.Lock()
	st.weight = c.steps[0]
	st.mu.Unlock()
	log.Printf("gate: start, v2=%d%%, steps=%v, max_error_rate=%g, max_p95=%gms, healthy_checks=%d, interval=%s, warmup=%s, start_delay=%s",
		c.steps[0], c.steps, c.maxErrorRate, c.maxP95ms, c.healthyChecks, c.interval, c.warmup, c.startDelay)

	healthy := 0
	startedAt := time.Now()
	changedAt := startedAt
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			srv.Shutdown(context.Background())
			return
		case <-ticker.C:
		}

		st.mu.Lock()
		state := st.state
		st.mu.Unlock()
		if state != stateProgressing {
			continue // откатили или докатили — решение принято, ждём нового релиза (рестарта гейта)
		}

		qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		rps, errR := promQuery(qctx, c.promURL, c.qRPS)
		errRate, errE := promQuery(qctx, c.promURL, c.qErrorRate)
		p95, errP := promQuery(qctx, c.promURL, c.qP95)
		cancel()

		// Нет данных об ошибках при наличии трафика = ошибок нет (ряд failure ещё не появился)
		if errors.Is(errE, errNoData) {
			errRate, errE = 0, nil
		}
		if errors.Is(errP, errNoData) {
			p95, errP = 0, nil
		}
		if errors.Is(errR, errNoData) {
			rps, errR = 0, nil
		}
		if err := errors.Join(errR, errE, errP); err != nil {
			st.decide("error")
			log.Printf("prometheus query failed, holding: %v", err)
			continue
		}
		st.mu.Lock()
		st.rps, st.errorRate, st.p95ms = rps, errRate, p95
		weight := st.weight
		st.mu.Unlock()

		switch {
		case rps < c.minRPS:
			st.decide("wait")
			log.Printf("v2=%d%%: rps=%.2f < %.2f, not enough traffic to judge, waiting", weight, rps, c.minRPS)

		case time.Since(startedAt) < c.startDelay+c.warmup:
			// Ни отката, ни повышения: ошибки (или их отсутствие) могут быть чужими
			st.decide("wait")
			log.Printf("v2=%d%%: release warming up %s/%s, old v2 pods may still be in the window (error_rate=%.3f, p95=%.0fms, rps=%.1f)",
				weight, time.Since(startedAt).Round(time.Second), c.startDelay+c.warmup, errRate, p95, rps)

		case errRate > c.maxErrorRate || p95 > c.maxP95ms:
			if err := k.setWeights(ctx, c, 0); err != nil {
				st.decide("error")
				log.Printf("ROLLBACK patch failed: %v", err)
				continue
			}
			st.decide("rollback")
			st.mu.Lock()
			st.weight, st.state = 0, stateRolledBack
			st.mu.Unlock()
			log.Printf("ROLLBACK: v2 %d%% -> 0%%: error_rate=%.3f (max %.3f), p95=%.0fms (max %.0f)",
				weight, errRate, c.maxErrorRate, p95, c.maxP95ms)

		case time.Since(changedAt) < c.warmup:
			// В окне rate() ещё есть данные до смены веса (или от старого пода v2):
			// откатить по ним можно, а повышать — нет
			st.decide("wait")
			log.Printf("v2=%d%%: warming up %s/%s (error_rate=%.3f, p95=%.0fms, rps=%.1f)",
				weight, time.Since(changedAt).Round(time.Second), c.warmup, errRate, p95, rps)

		default:
			healthy++
			if healthy < c.healthyChecks {
				st.decide("hold")
				log.Printf("v2=%d%%: healthy %d/%d (error_rate=%.3f, p95=%.0fms, rps=%.1f)",
					weight, healthy, c.healthyChecks, errRate, p95, rps)
				continue
			}
			healthy = 0
			if step+1 >= len(c.steps) {
				continue
			}
			next := c.steps[step+1]
			if err := k.setWeights(ctx, c, next); err != nil {
				st.decide("error")
				log.Printf("promote patch failed: %v", err)
				continue
			}
			step++
			changedAt = time.Now()
			st.mu.Lock()
			st.weight = next
			if next == 100 {
				st.state = statePromoted
			}
			st.mu.Unlock()
			if next == 100 {
				st.decide("done")
				log.Printf("PROMOTED: v2 %d%% -> 100%%, release complete", weight)
			} else {
				st.decide("promote")
				log.Printf("PROMOTE: v2 %d%% -> %d%% (error_rate=%.3f, p95=%.0fms, rps=%.1f)",
					weight, next, errRate, p95, rps)
			}
		}
	}
}
