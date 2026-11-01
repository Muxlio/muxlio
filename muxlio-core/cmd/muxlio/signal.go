package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

func cmdSignal(args []string) int {
	fs := flag.NewFlagSet("signal", flag.ContinueOnError)
	server := fs.String("server", os.Getenv("MUXLIO_SERVER"), "coordinator URL")
	name := fs.String("name", "", "signal name to deliver")
	dataPath := fs.String("data", "", "path to JSON payload file (or inline JSON)")

	if err := fs.Parse(reorderArgs(args)); err != nil {
		return 2
	}
	if fs.NArg() < 1 || *name == "" {
		fmt.Fprintln(os.Stderr, "usage: muxlio signal <run-id> --name <signal-name> [--data payload.json] [--server URL]")
		return 2
	}
	runID := fs.Arg(0)
	serverURL, token, err := resolveServer(*server, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 2
	}

	payload := []byte("{}")
	if *dataPath != "" {
		if fileExists(*dataPath) {
			b, err := os.ReadFile(*dataPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error reading data file: %v\n", err)
				return 1
			}
			payload = b
		} else {
			// Inline JSON string
			payload = []byte(*dataPath)
		}
	}

	endpoint := fmt.Sprintf("%s/v1/runs/%s/signals/%s", strings.TrimRight(serverURL, "/"), url.PathEscape(runID), url.PathEscape(*name))
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error creating request: %v\n", err)
		return 1
	}
	req.Header.Set("Content-Type", "application/json")
	setAuthHeader(req, token)
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error sending signal: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Fprintf(os.Stderr, "HTTP %d: %s\n", resp.StatusCode, raw)
		return 1
	}
	fmt.Println(strings.TrimSpace(string(raw)))
	return 0
}
