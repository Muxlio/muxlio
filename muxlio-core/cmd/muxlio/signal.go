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

func cmdSignal(args []string, dataDir string) int {
	fs := flag.NewFlagSet("signal", flag.ContinueOnError)
	server := fs.String("server", os.Getenv("MUXLIO_SERVER"), "coordinator URL")
	name := fs.String("name", "", "signal name to deliver")
	requestID := fs.String("request-id", "", "durable input request ID to respond to")
	list := fs.Bool("list-requests", false, "list durable input requests")
	key := fs.String("idempotency-key", "", "delivery key (required for input responses; reuse on retry)")
	dataPath := fs.String("data", "", "path to JSON payload file (or inline JSON)")

	if err := fs.Parse(reorderArgs(args)); err != nil {
		return 2
	}
	if fs.NArg() != 1 || (!*list && *name == "" && *requestID == "") || (*requestID != "" && (*name != "" || *key == "" || *list)) || (*list && *name != "") {
		fmt.Fprintln(os.Stderr, "usage: muxlio signal <run-id> (--name NAME | --list-requests | --request-id ID --idempotency-key KEY) [--data payload.json] [--server URL]")
		return 2
	}
	runID := fs.Arg(0)
	serverURL, token, err := resolveServer(*server, dataDir)
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
	method := http.MethodPost
	if *list || *requestID != "" {
		endpoint = fmt.Sprintf("%s/v1/runs/%s/input-requests", strings.TrimRight(serverURL, "/"), url.PathEscape(runID))
		if *list {
			method = http.MethodGet
		} else {
			endpoint += "/" + url.PathEscape(*requestID)
		}
	}
	req, err := http.NewRequest(method, endpoint, bytes.NewReader(payload))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error creating request: %v\n", err)
		return 1
	}
	req.Header.Set("Content-Type", "application/json")
	if *key != "" {
		req.Header.Set("Idempotency-Key", *key)
	}
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
