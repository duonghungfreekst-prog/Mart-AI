package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

func main() {
	baseDir, err := os.Getwd()
	if err != nil {
		baseDir = "."
	}
	engineDir := filepath.Join(baseDir, "muse_router_engine")

	binName := "muse_router_srv"
	if runtime.GOOS == "windows" {
		binName = "muse_router.exe"
	}
	engineBin := filepath.Join(engineDir, binName)

	if _, errStat := os.Stat(engineBin); errStat != nil {
		if goPath, errLook := exec.LookPath("go"); errLook == nil {
			fmt.Printf("[MART-AI] Compiling muse_router_engine (%s)...\n", binName)
			buildCmd := exec.Command(goPath, "build", "-ldflags=-s -w", "-o", engineBin, ".")
			buildCmd.Dir = engineDir
			buildCmd.Env = os.Environ()
			buildCmd.Stdout = os.Stdout
			buildCmd.Stderr = os.Stderr
			if errBuild := buildCmd.Run(); errBuild != nil {
				fmt.Fprintf(os.Stderr, "[MART-AI] Build failed: %v\n", errBuild)
			}
		}
	}

	if _, errStat := os.Stat(engineBin); errStat == nil {
		runCmd := exec.Command(engineBin)
		runCmd.Dir = baseDir
		runCmd.Env = os.Environ()
		runCmd.Stdout = os.Stdout
		runCmd.Stderr = os.Stderr
		runCmd.Stdin = os.Stdin
		if errRun := runCmd.Run(); errRun != nil {
			fmt.Fprintf(os.Stderr, "[MART-AI] Engine exited: %v — falling back to minimal server\n", errRun)
		} else {
			return
		}
	}

	runFallbackServer(baseDir)
}

func runFallbackServer(baseDir string) {
	port := os.Getenv("PORT")
	if port == "" {
		port = os.Getenv("MUSE_ENGINE_PORT")
	}
	if port == "" {
		port = "8088"
	}
	host := "0.0.0.0"
	if os.Getenv("PORT") == "" && os.Getenv("RENDER") == "" {
		host = "127.0.0.1"
	}

	webDir := filepath.Join(baseDir, "muse_router_engine", "web")
	fileServer := http.FileServer(http.Dir(webDir))
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status":  "starting",
			"service": "mart-ai",
			"note":    "engine is being compiled",
		})
	})

	mux.HandleFunc("/api/v1/pool/oauth-link", func(w http.ResponseWriter, r *http.Request) {
		noRedir := &http.Client{
			Timeout: 12 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
		req, _ := http.NewRequest(http.MethodGet, "https://muse.ai/api/oidc/start?return_to=%2F", nil)
		req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; MartAI/1.0)")
		location := "https://muse.ai/api/oidc/start?return_to=%2F"
		stateVal := ""
		if resp, errDo := noRedir.Do(req); errDo == nil {
			if loc := resp.Header.Get("Location"); loc != "" {
				location = loc
			}
			resp.Body.Close()
		}
		if u, errP := url.Parse(location); errP == nil {
			stateVal = u.Query().Get("state")
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"session_id":   fmt.Sprintf("oauth-%d", time.Now().UnixMilli()),
			"login_url":    location,
			"state":        stateVal,
			"app_id":       "1221469106811220",
			"redirect_uri": "https://muse.ai/api/oidc/callback",
		})
	})

	mux.Handle("/", fileServer)

	addr := host + ":" + port
	fmt.Printf("[MART-AI] Fallback server listening at http://%s\n", addr)
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       120 * time.Second,
		WriteTimeout:      120 * time.Second,
	}
	_ = srv.ListenAndServe()
}
