// batch — заглушка "ночной аналитики": бесконечно жжёт CPU и держит память,
// ничего полезного не делая. Нужна, чтобы создавать давление на ноду.
//
// Настройка через env:
//
//	PORT              порт /health и /metrics (8080)
//	CPU_WORKERS       сколько горутин крутят CPU (1); 0 — CPU не жечь
//	CPU_DUTY          доля времени, которую каждая горутина занята, 0..1 (1.0)
//	MEM_TARGET_MB     сколько памяти набрать и держать (64); 0 — не набирать
//	MEM_STEP_MB       сколько добавлять за шаг (16)
//	MEM_STEP_INTERVAL пауза между шагами (2s) — рост постепенный, чтобы
//	                  eviction/OOMKill было видно на графиках, а не мгновенно
package main

import (
	"context"
	"errors"
	"log"
	"math"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	mib      = 1 << 20
	pageSize = 4096
	// Окно, внутри которого горутина CPU_DUTY времени занята, остальное спит.
	cpuSlice = 100 * time.Millisecond
)

var (
	cpuIterations = promauto.NewCounter(prometheus.CounterOpts{
		Name: "batch_cpu_iterations_total",
		Help: "Iterations of the CPU burn loop. Its rate is batch throughput: drops under CPU contention or throttling.",
	})
	memHeld = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "batch_memory_held_bytes",
		Help: "Bytes allocated and touched (resident) by the memory hog.",
	})
	memTarget = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "batch_memory_target_bytes",
		Help: "MEM_TARGET_MB in bytes.",
	})
	cpuWorkers = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "batch_cpu_workers",
		Help: "CPU_WORKERS.",
	})
)

// sink не даёт компилятору выкинуть вычисления как неиспользуемые.
var sink float64

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func mustInt(key, def string) int {
	v, err := strconv.Atoi(env(key, def))
	if err != nil || v < 0 {
		log.Fatalf("bad %s: %q", key, os.Getenv(key))
	}
	return v
}

func main() {
	workers := mustInt("CPU_WORKERS", "1")
	targetMB := mustInt("MEM_TARGET_MB", "64")
	stepMB := mustInt("MEM_STEP_MB", "16")
	duty, err := strconv.ParseFloat(env("CPU_DUTY", "1.0"), 64)
	if err != nil || duty <= 0 || duty > 1 {
		log.Fatalf("bad CPU_DUTY: %q (want 0 < x <= 1)", os.Getenv("CPU_DUTY"))
	}
	stepEvery, err := time.ParseDuration(env("MEM_STEP_INTERVAL", "2s"))
	if err != nil || stepEvery <= 0 {
		log.Fatalf("bad MEM_STEP_INTERVAL: %q", os.Getenv("MEM_STEP_INTERVAL"))
	}
	if stepMB == 0 {
		stepMB = 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cpuWorkers.Set(float64(workers))
	memTarget.Set(float64(targetMB) * mib)
	log.Printf("batch: cpu workers=%d duty=%.2f, memory target=%dMiB step=%dMiB/%s",
		workers, duty, targetMB, stepMB, stepEvery)

	for i := 0; i < workers; i++ {
		go burnCPU(ctx, duty)
	}
	go hogMemory(ctx, int64(targetMB)*mib, int64(stepMB)*mib, stepEvery)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok"))
	})
	mux.Handle("/metrics", promhttp.Handler())
	srv := &http.Server{Addr: ":" + env("PORT", "8080"), Handler: mux}

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http: %v", err)
		}
	}()

	<-ctx.Done()
	log.Print("batch: SIGTERM, shutting down")
	shCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(shCtx)
}

// burnCPU занимает ядро на duty долю каждого окна cpuSlice.
// Каждые 1000 операций — одна "итерация" в метрике: её rate показывает,
// сколько полезной (бесполезной) работы batch реально успевает сделать.
func burnCPU(ctx context.Context, duty float64) {
	busy := time.Duration(float64(cpuSlice) * duty)
	x := 1.0001
	for {
		start := time.Now()
		var n int
		for time.Since(start) < busy {
			for i := 0; i < 1000; i++ {
				x = math.Sqrt(x*x + 1.0001)
			}
			n++
		}
		cpuIterations.Add(float64(n))
		sink = x

		rest := cpuSlice - time.Since(start)
		if rest <= 0 {
			rest = 0
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(rest):
		}
	}
}

// hogMemory шагами набирает target байт и держит их до конца жизни процесса.
// Каждая страница записывается: make() только резервирует адреса, а в RSS
// (и в working set, по которому смотрит kubelet) память попадает, когда
// страницу реально тронули. Ссылки на куски живут в held, поэтому GC их не соберёт.
func hogMemory(ctx context.Context, target, step int64, every time.Duration) {
	var held [][]byte
	var total int64
	t := time.NewTicker(every)
	defer t.Stop()

	for {
		if total < target {
			n := min(step, target-total)
			b := make([]byte, n)
			for i := 0; i < len(b); i += pageSize {
				b[i] = byte(i>>12) | 1
			}
			held = append(held, b)
			total += n
			memHeld.Set(float64(total))
			log.Printf("batch: holding %dMiB / %dMiB", total/mib, target/mib)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
