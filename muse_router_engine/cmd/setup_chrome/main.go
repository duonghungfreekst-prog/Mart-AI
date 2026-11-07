package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const fallbackHeadlessShellURL = "https://storage.googleapis.com/chrome-for-testing-public/131.0.6778.204/linux64/chrome-headless-shell-linux64.zip"

func resolveStableLinuxHeadlessShellURL(client *http.Client) string {
	resp, err := client.Get("https://googlechromelabs.github.io/chrome-for-testing/last-known-good-versions-with-downloads.json")
	if err != nil {
		return fallbackHeadlessShellURL
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fallbackHeadlessShellURL
	}
	var doc struct {
		Channels struct {
			Stable struct {
				Downloads map[string][]struct {
					Platform string `json:"platform"`
					URL      string `json:"url"`
				} `json:"downloads"`
			} `json:"Stable"`
		} `json:"channels"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4*1024*1024)).Decode(&doc); err != nil {
		return fallbackHeadlessShellURL
	}
	for _, item := range doc.Channels.Stable.Downloads["chrome-headless-shell"] {
		if item.Platform == "linux64" && strings.HasPrefix(item.URL, "https://") {
			return item.URL
		}
	}
	return fallbackHeadlessShellURL
}

func main() {
	outDir := "../chrome_bin"
	if len(os.Args) > 1 && strings.TrimSpace(os.Args[1]) != "" {
		outDir = strings.TrimSpace(os.Args[1])
	}
	targetExe := filepath.Join(outDir, "chrome-headless-shell-linux64", "chrome-headless-shell")
	if st, err := os.Stat(targetExe); err == nil && st.Size() > 1024*1024 {
		fmt.Printf("[SETUP_CHROME] Already exists: %s\n", targetExe)
		return
	}
	if err := os.MkdirAll(outDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "[SETUP_CHROME] mkdir error: %v\n", err)
		os.Exit(1)
	}

	client := &http.Client{Timeout: 180 * time.Second}
	dlURL := resolveStableLinuxHeadlessShellURL(client)
	fmt.Printf("[SETUP_CHROME] Downloading %s ...\n", dlURL)

	zipPath := filepath.Join(outDir, "headless_shell.zip")
	resp, err := client.Get(dlURL)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		fmt.Fprintf(os.Stderr, "[SETUP_CHROME] download failed: %v\n", err)
		os.Exit(1)
	}
	zf, err := os.Create(zipPath)
	if err != nil {
		resp.Body.Close()
		fmt.Fprintf(os.Stderr, "[SETUP_CHROME] create zip file error: %v\n", err)
		os.Exit(1)
	}
	if _, err := io.Copy(zf, resp.Body); err != nil {
		zf.Close()
		resp.Body.Close()
		fmt.Fprintf(os.Stderr, "[SETUP_CHROME] write zip error: %v\n", err)
		os.Exit(1)
	}
	zf.Close()
	resp.Body.Close()
	defer os.Remove(zipPath)

	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[SETUP_CHROME] open zip error: %v\n", err)
		os.Exit(1)
	}
	defer zr.Close()

	cleanBase := filepath.Clean(outDir) + string(os.PathSeparator)
	for _, f := range zr.File {
		destPath := filepath.Join(outDir, filepath.FromSlash(f.Name))
		if !strings.HasPrefix(filepath.Clean(destPath), strings.TrimSuffix(cleanBase, string(os.PathSeparator))) {
			continue
		}
		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(destPath, 0755)
			continue
		}
		_ = os.MkdirAll(filepath.Dir(destPath), 0755)
		rc, err := f.Open()
		if err != nil {
			continue
		}
		mode := f.Mode()
		if mode == 0 || strings.HasSuffix(f.Name, "chrome-headless-shell") || strings.HasSuffix(f.Name, "chrome_crashpad_handler") {
			mode = 0755
		}
		df, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			rc.Close()
			continue
		}
		_, _ = io.Copy(df, rc)
		df.Close()
		rc.Close()
	}
	_ = os.Chmod(targetExe, 0755)
	fmt.Printf("[SETUP_CHROME] Extracted ready: %s\n", targetExe)
}

