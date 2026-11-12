package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

func cmdUI(args []string, globalJSON bool) int {
	fs := flag.NewFlagSet("ui", flag.ContinueOnError)
	server := fs.String("server", os.Getenv("MUXLIO_SERVER"), "coordinator URL (default: http://127.0.0.1:7600)")
	port := fs.Int("port", 7601, "port for UI server (default: 7601)")
	host := fs.String("host", "127.0.0.1", "host to bind UI server (default: 127.0.0.1)")
	openBrowser := fs.Bool("open", true, "open browser automatically")
	jsonOut := fs.Bool("json", globalJSON, "emit machine-readable JSON output")

	if err := fs.Parse(reorderArgs(args)); err != nil {
		return 2
	}

	coordinatorURL := *server
	if coordinatorURL == "" {
		coordinatorURL = "http://127.0.0.1:7600"
	}

	uiURL := fmt.Sprintf("http://%s:%d", *host, *port)

	// Check if UI is already listening
	client := &http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get(uiURL)
	if err == nil {
		resp.Body.Close()
		if *jsonOut {
			out, _ := json.Marshal(map[string]any{
				"status":         "running",
				"url":            uiURL,
				"coordinatorURL": coordinatorURL,
			})
			fmt.Println(string(out))
			return 0
		}
		fmt.Printf("Muxlio UI is already running at %s\n", uiURL)
		if *openBrowser {
			_ = openURL(uiURL)
		}
		return 0
	}

	// Find the compiled UI server.
	uiScript := findUIScript()
	if uiScript == "" {
		fmt.Fprintf(os.Stderr, "error: UI assets not found. Check that ~/.muxlio/share/muxlio/ui or muxlio-ui exists.\n")
		return 1
	}

	if *jsonOut {
		out, _ := json.Marshal(map[string]any{
			"status":         "starting",
			"url":            uiURL,
			"coordinatorURL": coordinatorURL,
			"script":         uiScript,
		})
		fmt.Println(string(out))
	} else {
		fmt.Printf("Starting Muxlio UI at %s (coordinator: %s)...\n", uiURL, coordinatorURL)
	}

	cmd := exec.Command("node", uiScript)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("PORT=%d", *port),
		fmt.Sprintf("HOST=%s", *host),
		fmt.Sprintf("MUXLIO_API_URL=%s", coordinatorURL),
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if *openBrowser {
		go func() {
			time.Sleep(600 * time.Millisecond)
			_ = openURL(uiURL)
		}()
	}

	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "UI server exited: %v\n", err)
		return 1
	}
	return 0
}

func findUIScript() string {
	candidates := []string{
		os.Getenv("MUXLIO_UI"),
		filepath.Join(defaultRuntime(), "share", "muxlio", "ui", "dist", "server.mjs"),
		filepath.Join(defaultRuntime(), "ui", "dist", "server.mjs"),
	}

	// Also check relative to executable
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		prefix := filepath.Dir(filepath.Dir(exe))
		candidates = append(candidates,
			filepath.Join(prefix, "share", "muxlio", "ui", "dist", "server.mjs"),
			filepath.Join(prefix, "muxlio-ui", "dist", "server.mjs"),
		)
	}

	// Check current directory
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(cwd, "muxlio-ui", "dist", "server.mjs"),
			filepath.Join(cwd, "..", "muxlio-ui", "dist", "server.mjs"),
		)
	}

	for _, c := range candidates {
		if c != "" && fileExists(c) {
			return c
		}
	}
	return ""
}

func openURL(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "linux":
		return exec.Command("xdg-open", url).Start()
	default:
		return nil
	}
}
