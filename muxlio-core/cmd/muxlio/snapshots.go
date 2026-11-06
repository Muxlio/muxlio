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

func cmdSnapshots(args []string, globalDataDir string, globalJSON bool) int {
	fs := flag.NewFlagSet("snapshots", flag.ContinueOnError)
	server := fs.String("server", os.Getenv("MUXLIO_SERVER"), "coordinator URL")
	current := fs.Bool("current", false, "select latest intermediate snapshot per output")
	activation := fs.String("activation", "", "filter activation ID")
	attempt := fs.String("attempt", "", "filter attempt ID")
	since := fs.Int("since", 0, "filter events strictly after sequence number")
	limit := fs.Int("limit", 100, "maximum number of events to fetch")
	dataDir := fs.String("data-dir", globalDataDir, "runtime data directory (default: ~/.muxlio)")
	jsonOut := fs.Bool("json", globalJSON, "emit machine-readable JSON output")

	if err := fs.Parse(reorderArgs(args)); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: muxlio snapshots <run-id> [--current] [--activation ID] [--attempt ID] [--since seq] [--limit N] [--server URL] [--json]")
		return 2
	}
	runID := fs.Arg(0)
	if *limit <= 0 || *limit > 200 {
		*limit = 200
	}

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
				if _, err := store.GetRun(runID); err != nil {
					fmt.Fprintf(os.Stderr, "error: run not found: %s\n", runID)
					return 1
				}
				evs, err := state.ListSnapshots(store, runID, *activation, *attempt, *current)
				page := make([]state.Event, 0)
				for _, event := range evs {
					if event.Seq > *since {
						page = append(page, event)
					}
				}
				if err == nil {
					more := len(page) > *limit
					if more {
						page = page[:*limit]
					}
					next := *since
					if len(page) > 0 {
						next = page[len(page)-1].Seq
					}
					if *jsonOut {
						out, _ := json.MarshalIndent(map[string]any{"snapshots": page, "nextAfter": next, "hasMore": more}, "", "  ")
						fmt.Println(string(out))
						return 0
					}
					return renderSnapshots(page, false)
				}
			}
		}
	}
	serverURL, token, err := resolveServer(*server, *dataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 2
	}

	endpoint := fmt.Sprintf("%s/v1/runs/%s/snapshots?after=%d&limit=%d&current=%t&activationId=%s&attemptId=%s",
		strings.TrimRight(serverURL, "/"), url.PathEscape(runID), *since, *limit, *current, url.QueryEscape(*activation), url.QueryEscape(*attempt))
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
		Events []state.Event `json:"snapshots"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		fmt.Fprintf(os.Stderr, "error decoding events response: %v\n", err)
		return 1
	}
	return renderSnapshots(data.Events, false)
}

func renderSnapshots(events []state.Event, jsonOut bool) int {
	if jsonOut {
		out, _ := json.MarshalIndent(map[string]any{"snapshots": events}, "", "  ")
		fmt.Println(string(out))
		return 0
	}
	fmt.Println("Intermediate snapshots (not committed final outputs):")
	return renderEvents(events, false)
}
