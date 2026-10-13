// loadgen — подопытный клиент shop: шлёт POST /order на api по DNS-имени Service
// с постоянным темпом и отдаёт результат каждого запроса в /metrics.
//
// Конфигурация через env:
//   TARGET_URL    куда стрелять (по умолчанию http://shop-api.shop.svc.cluster.local:8080/order)
//   RPS           запросов в секунду (20)
//   TIMEOUT       таймаут одного запроса, включая connect (2s)
//   KEEPALIVE     переиспользовать TCP-соединения (false: каждый запрос — новое соединение,
//                 новый проход через DNS и правила kube-proxy)
//   MAX_INFLIGHT  потолок одновременных запросов (100); сверх него тик пропускается
//   PORT          порт /metrics и /health (8080)
//
// Метрики пишутся вручную в текстовом формате Prometheus — только стандартная библиотека.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// Результаты запроса (метка result)
const (
	resOK        = "ok"         // 2xx
	resHTTPError = "http_error" // ответ пришёл, но не 2xx
	resTimeout   = "timeout"    // не уложились в TIMEOUT
	resConnError = "conn_error" // connect refused / reset / DNS и прочее до ответа
	resSkipped   = "skipped"    // упёрлись в MAX_INFLIGHT, запрос не отправлен
)

var buckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5}

type seriesKey struct{ result, code string }

type metrics struct {
	mu       sync.Mutex
	counts   map[seriesKey]uint64
	bucketN  []uint64 // некумулятивные счётчики по корзинам; +Inf — последний элемент
	sum      float64
	count    uint64
	inflight int64
	rps      float64
}

func newMetrics(rps float64) *metrics {
	m := &metrics{
		counts:  map[seriesKey]uint64{},
		bucketN: make([]uint64, len(buckets)+1),
		rps:     rps,
	}
	// Ряды ошибок существуют с нуля: rate() показывает 0, а не "no data"
	for _, r := range []string{resTimeout, resConnError, resSkipped} {
		m.counts[seriesKey{r, "none"}] = 0
	}
	return m
}

func (m *metrics) inc(result, code string) {
	m.mu.Lock()
	m.counts[seriesKey{result, code}]++
	m.mu.Unlock()
}

// observe — задержка только успешных запросов: латентность ошибок мерить бессмысленно
func (m *metrics) observe(d time.Duration) {
	s := d.Seconds()
	m.mu.Lock()
	defer m.mu.Unlock()
	i := sort.SearchFloat64s(buckets, s) // первая корзина с границей >= s
	m.bucketN[i]++
	m.sum += s
	m.count++
}

func (m *metrics) addInflight(d int64) {
	m.mu.Lock()
	m.inflight += d
	m.mu.Unlock()
}

func (m *metrics) write(w io.Writer) {
	m.mu.Lock()
	defer m.mu.Unlock()

	fmt.Fprintln(w, "# HELP loadgen_requests_total Requests sent by loadgen, by result and HTTP code.")
	fmt.Fprintln(w, "# TYPE loadgen_requests_total counter")
	keys := make([]seriesKey, 0, len(m.counts))
	for k := range m.counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].result != keys[j].result {
			return keys[i].result < keys[j].result
		}
		return keys[i].code < keys[j].code
	})
	for _, k := range keys {
		fmt.Fprintf(w, "loadgen_requests_total{result=%q,code=%q} %d\n", k.result, k.code, m.counts[k])
	}

	fmt.Fprintln(w, "# HELP loadgen_request_duration_seconds Latency of successful (2xx) requests.")
	fmt.Fprintln(w, "# TYPE loadgen_request_duration_seconds histogram")
	var cum uint64
	for i, b := range buckets {
		cum += m.bucketN[i]
		fmt.Fprintf(w, "loadgen_request_duration_seconds_bucket{le=%q} %d\n", strconv.FormatFloat(b, 'f', -1, 64), cum)
	}
	cum += m.bucketN[len(buckets)]
	fmt.Fprintf(w, "loadgen_request_duration_seconds_bucket{le=\"+Inf\"} %d\n", cum)
	fmt.Fprintf(w, "loadgen_request_duration_seconds_sum %g\n", m.sum)
	fmt.Fprintf(w, "loadgen_request_duration_seconds_count %d\n", m.count)

	fmt.Fprintln(w, "# HELP loadgen_inflight_requests Requests currently in flight.")
	fmt.Fprintln(w, "# TYPE loadgen_inflight_requests gauge")
	fmt.Fprintf(w, "loadgen_inflight_requests %d\n", m.inflight)

	fmt.Fprintln(w, "# HELP loadgen_target_rps Configured request rate.")
	fmt.Fprintln(w, "# TYPE loadgen_target_rps gauge")
	fmt.Fprintf(w, "loadgen_target_rps %g\n", m.rps)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	target := env("TARGET_URL", "http://shop-api.shop.svc.cluster.local:8080/order")
	rps, err := strconv.ParseFloat(env("RPS", "20"), 64)
	if err != nil || rps <= 0 {
		log.Fatalf("bad RPS: %q", os.Getenv("RPS"))
	}
	timeout, err := time.ParseDuration(env("TIMEOUT", "2s"))
	if err != nil {
		log.Fatalf("bad TIMEOUT: %v", err)
	}
	keepAlive, _ := strconv.ParseBool(env("KEEPALIVE", "false"))
	maxInflight, err := strconv.Atoi(env("MAX_INFLIGHT", "100"))
	if err != nil || maxInflight <= 0 {
		log.Fatalf("bad MAX_INFLIGHT: %q", os.Getenv("MAX_INFLIGHT"))
	}
	port := env("PORT", "8080")

	m := newMetrics(rps)
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:         (&net.Dialer{Timeout: timeout}).DialContext,
			DisableKeepAlives:   !keepAlive,
			MaxIdleConnsPerHost: maxInflight,
		},
	}
	body := []byte(`{"item":"loadgen","qty":1}`)

	fire := func(ctx context.Context) {
		m.addInflight(1)
		defer m.addInflight(-1)

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
		if err != nil {
			m.inc(resConnError, "none")
			log.Printf("build request: %v", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")

		start := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return // остановка loadgen, а не ошибка сервиса
			}
			var nerr net.Error
			if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &nerr) && nerr.Timeout()) {
				m.inc(resTimeout, "none")
			} else {
				m.inc(resConnError, "none")
			}
			log.Printf("request failed: %v", err)
			return
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		code := strconv.Itoa(resp.StatusCode)
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			m.inc(resOK, code)
			m.observe(time.Since(start))
		} else {
			m.inc(resHTTPError, code)
			log.Printf("request got HTTP %d", resp.StatusCode)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		m.write(w)
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "ok")
	})
	srv := &http.Server{Addr: ":" + port, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("metrics server: %v", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	log.Printf("loadgen: target=%s rps=%g timeout=%s keepalive=%t max_inflight=%d",
		target, rps, timeout, keepAlive, maxInflight)

	// Постоянный темп: тик раз в 1/RPS. Если сервис тормозит и запросы копятся,
	// тики сверх MAX_INFLIGHT считаются skipped — клиент не превращается в DDoS.
	ticker := time.NewTicker(time.Duration(float64(time.Second) / rps))
	defer ticker.Stop()
	sem := make(chan struct{}, maxInflight)
	var wg sync.WaitGroup

loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-ticker.C:
			select {
			case sem <- struct{}{}:
				wg.Add(1)
				go func() {
					defer wg.Done()
					defer func() { <-sem }()
					fire(ctx)
				}()
			default:
				m.inc(resSkipped, "none")
			}
		}
	}

	log.Print("loadgen: stopping")
	wg.Wait()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(shutdownCtx)
}
