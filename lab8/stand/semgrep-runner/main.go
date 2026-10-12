// semgrep-runner: HTTP-обёртка над semgrep для n8n.
// POST /scan {owner, repo, sha, paths[]} -> клонирует коммит, сканирует изменённые файлы, отдаёт JSON.
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"
)

var (
	nameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	shaRe  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	extRe  = regexp.MustCompile(`\.(js|jsx|ts|tsx|mjs|cjs)$`)
	// один скан за раз: на 2 vCPU параллельные semgrep только мешают друг другу
	mu sync.Mutex
)

type scanReq struct {
	Owner string   `json:"owner"`
	Repo  string   `json:"repo"`
	SHA   string   `json:"sha"`
	Paths []string `json:"paths"`
}

type finding struct {
	CheckID  string `json:"check_id"`
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Lines    string `json:"lines"`
}

type semgrepOut struct {
	Results []struct {
		CheckID string `json:"check_id"`
		Path    string `json:"path"`
		Start   struct {
			Line int `json:"line"`
		} `json:"start"`
		Extra struct {
			Message  string `json:"message"`
			Severity string `json:"severity"`
			Lines    string `json:"lines"`
		} `json:"extra"`
	} `json:"results"`
	Errors []json.RawMessage `json:"errors"`
}

func run(ctx context.Context, dir string, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "SEMGREP_SEND_METRICS=off")
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	return so.Bytes(), se.Bytes(), err
}

func fail(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func scan(w http.ResponseWriter, r *http.Request) {
	var req scanReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		fail(w, 400, "bad json")
		return
	}
	if !nameRe.MatchString(req.Owner) || !nameRe.MatchString(req.Repo) || !shaRe.MatchString(req.SHA) {
		fail(w, 400, "bad owner/repo/sha")
		return
	}

	mu.Lock()
	defer mu.Unlock()

	ctx, cancel := context.WithTimeout(r.Context(), 150*time.Second)
	defer cancel()

	dir, err := os.MkdirTemp("", "scan-")
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer os.RemoveAll(dir)

	// токен передаём заголовком, а не в URL: так он не попадает в .git/config и в сообщения об ошибках
	auth := "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+os.Getenv("GITHUB_TOKEN")))
	url := "https://github.com/" + req.Owner + "/" + req.Repo + ".git"
	for _, step := range [][]string{
		{"init", "-q"},
		{"-c", "http.extraHeader=" + auth, "fetch", "-q", "--depth", "1", url, req.SHA},
		{"checkout", "-q", "FETCH_HEAD"},
	} {
		if _, se, err := run(ctx, dir, "git", step...); err != nil {
			fail(w, 502, "git: "+strings.TrimSpace(string(se)))
			return
		}
	}

	// сканируем только файлы PR, и только код
	var targets []string
	for _, p := range req.Paths {
		p = path.Clean(p)
		if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "..") || !extRe.MatchString(p) {
			continue
		}
		if _, err := os.Stat(dir + "/" + p); err == nil {
			targets = append(targets, p)
		}
	}

	resp := map[string]any{"scanned": targets, "results": []finding{}, "errors": []json.RawMessage{}, "count": 0}
	if len(targets) > 0 {
		args := append([]string{"scan", "--config", "/rules", "--json", "--metrics=off",
			"--disable-version-check", "--quiet", "--timeout", "30"}, targets...)
		so, se, err := run(ctx, dir, "semgrep", args...)
		if err != nil {
			fail(w, 500, "semgrep: "+strings.TrimSpace(string(se)))
			return
		}
		var out semgrepOut
		if err := json.Unmarshal(so, &out); err != nil {
			fail(w, 500, "semgrep output: "+err.Error())
			return
		}
		res := []finding{}
		for _, x := range out.Results {
			res = append(res, finding{x.CheckID, x.Path, x.Start.Line, x.Extra.Severity, x.Extra.Message, x.Extra.Lines})
		}
		resp["results"], resp["errors"], resp["count"] = res, out.Errors, len(res)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func main() {
	http.HandleFunc("/scan", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			fail(w, 405, "POST only")
			return
		}
		scan(w, r)
	})
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	log.Println("semgrep-runner on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
