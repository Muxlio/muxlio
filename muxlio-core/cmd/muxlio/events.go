package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
)

func cmdEvents(args []string, globalDataDir string, globalJSON bool) int {
	fs := flag.NewFlagSet("events", flag.ContinueOnError)
	server := fs.String("server", os.Getenv("MUXLIO_SERVER"), "coordinator URL")
	since := fs.Int("since", 0, "filter events strictly after sequence number")
	limit := fs.Int("limit", 100, "maximum number of events to fetch")
	dataDir := fs.String("data-dir", globalDataDir, "runtime data directory (default: ~/.muxlio)")
	jsonOut := fs.Bool("json", globalJSON, "emit machine-readable JSON output")

	if err := fs.Parse(reorderArgs(args)); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: muxlio events <run-id> [--since seq] [--limit N] [--server URL] [--json]")
		return 2
	}
	runID := fs.Arg(0)

	// Try local database first if server is not explicitly set
	if *server == "" {
		root := *dataDir
		if root == "" {
			root = defaultRuntime()
		}
		dbPath := filepath.Join(root, "data", "coordinator.db")
		if fileExists(dbPath) {
			store, err := openStore(root)
			if err == nil {
				defer store.Close()
				evs, err := store.GetEvents(runID, *since, *limit)
				if err == nil && len(evs) > 0 {
					return renderEvents(evs, *jsonOut)
				}
			}
		}
	}
	serverURL, token, err := resolveServer(*server, *dataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 2
	}

	endpoint := fmt.Sprintf("%s/v1/runs/%s/events?after=%d&limit=%d",
		strings.TrimRight(serverURL, "/"), url.PathEscape(runID), *since, *limit)
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	setAuthHeader(req, token)
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error connecting to coordinator: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading response: %v\n", err)
		return 1
	}
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "HTTP %d: %s\n", resp.StatusCode, raw)
		return 1
	}

	if *jsonOut {
		fmt.Println(strings.TrimSpace(string(raw)))
		return 0
	}

	var data struct {
		Events []state.Event `json:"events"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		fmt.Fprintf(os.Stderr, "error decoding events response: %v\n", err)
		return 1
	}
	return renderEvents(data.Events, false)
}

func renderEvents(events []state.Event, jsonOut bool) int {
	if jsonOut {
		out, _ := json.MarshalIndent(map[string]any{"events": events}, "", "  ")
		fmt.Println(string(out))
		return 0
	}
	if len(events) == 0 {
		fmt.Println("no events")
		return 0
	}
	for _, e := range events {
		subj := ""
		if e.SubjectID != "" {
			subj = " [" + e.SubjectID + "]"
		}
		payloadStr := ""
		if len(e.Payload) > 0 {
			b, _ := json.Marshal(e.Payload)
			payloadStr = " " + string(b)
		}
		fmt.Printf("#%-4d %s %-24s%s%s\n", e.Seq, e.Timestamp, e.Type, subj, payloadStr)
	}
	return 0
}
