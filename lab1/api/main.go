// Cервис для Лабы 1 (namespaces/cgroups/capabilities/seccomp).
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"
)

func main() {
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}

	http.HandleFunc("/health", handleHealth)
	http.HandleFunc("/eat", handleEat)
	http.HandleFunc("/burn", handleBurn)

	log.Printf("api: слушаю %s (pid=%d)", addr, os.Getpid())
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatal(err)
	}
}

// GET /health — простая проверка живости.
func handleHealth(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, "ok")
}

// GET /eat?mb=N — выделяет N мегабайт и держит их в памяти до конца запроса,
// чтобы можно было спровоцировать OOM при ограничении cgroup memory.max.
func handleEat(w http.ResponseWriter, r *http.Request) {
	mbStr := r.URL.Query().Get("mb")
	mb, err := strconv.Atoi(mbStr)
	if err != nil || mb <= 0 {
		http.Error(w, "укажи ?mb=N, N > 0", http.StatusBadRequest)
		return
	}

	const mib = 1024 * 1024
	buf := make([]byte, mb*mib)
	// Трогаем каждую страницу, иначе память останется не закоммиченной
	// (виртуальной) и не будет реально учтена cgroup/RSS.
	for i := 0; i < len(buf); i += 4096 {
		buf[i] = 1
	}

	// Держим выделенную память некоторое время, чтобы её можно было
	// понаблюдать (top, cgroup memory.current) и/или поймать OOM killer.
	holdSeconds := 30
	if s := r.URL.Query().Get("hold"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v >= 0 {
			holdSeconds = v
		}
	}
	time.Sleep(time.Duration(holdSeconds) * time.Second)

	fmt.Fprintf(w, "allocated %d MB, held %d s, sum=%d\n", mb, holdSeconds, sumBytes(buf))
}

// защищаем от того, что компилятор выкинет "неиспользуемый" буфер.
func sumBytes(b []byte) int {
	var s int
	for _, v := range b {
		s += int(v)
	}
	return s
}

// GET /burn?seconds=N — грузит одно ядро CPU бесконечным циклом (по умолчанию 30с),
// чтобы можно было увидеть CPU throttling при ограничении cgroup cpu.max.
func handleBurn(w http.ResponseWriter, r *http.Request) {
	seconds := 30
	if s := r.URL.Query().Get("seconds"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v > 0 {
			seconds = v
		}
	}

	deadline := time.Now().Add(time.Duration(seconds) * time.Second)
	var x uint64
	for time.Now().Before(deadline) {
		x++ // busy loop — не спим, не уступаем ядро
	}

	fmt.Fprintf(w, "burned 1 core for %d s, counter=%d\n", seconds, x)
}

