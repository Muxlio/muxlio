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

type artifactEntry struct {
	ActivationID string `json:"activationId"`
	NodeID       string `json:"nodeId"`
	Name         string `json:"name"`
	Digest       string `json:"digest,omitempty"`
	Ref          string `json:"ref"`
}

func cmdArtifacts(args []string, globalDataDir string, globalJSON bool) int {
	fs := flag.NewFlagSet("artifacts", flag.ContinueOnError)
	server := fs.String("server", os.Getenv("MUXLIO_SERVER"), "coordinator URL")
	dataDir := fs.String("data-dir", globalDataDir, "runtime data directory (default: ~/.muxlio)")
	jsonOut := fs.Bool("json", globalJSON, "emit machine-readable JSON output")

	if err := fs.Parse(reorderArgs(args)); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: muxlio artifacts <run-id> [--server URL] [--json]")
		return 2
	}
	runID := fs.Arg(0)

	var activations []state.Activation
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
				acts, err := store.ListActivations(runID)
				if err == nil {
					activations = acts
				}
			}
		}
	}

	if len(activations) == 0 {
		serverURL, token, err := resolveServer(*server, *dataDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 2
		}
		endpoint := fmt.Sprintf("%s/v1/runs/%s/activations", strings.TrimRight(serverURL, "/"), url.PathEscape(runID))
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
		var data struct {
			Activations []state.Activation `json:"activations"`
		}
		if err := json.Unmarshal(raw, &data); err != nil {
			fmt.Fprintf(os.Stderr, "error decoding activations: %v\n", err)
			return 1
		}
		activations = data.Activations
	}

	artifacts := collectArtifactsFromActivations(activations)

	if *jsonOut {
		out, _ := json.MarshalIndent(map[string]any{
			"runId":     runID,
			"artifacts": artifacts,
		}, "", "  ")
		fmt.Println(string(out))
		return 0
	}

	if len(artifacts) == 0 {
		fmt.Println("no artifacts found for run", runID)
		return 0
	}

	fmt.Printf("Artifacts for run %s (%d found):\n\n", runID, len(artifacts))
	for _, a := range artifacts {
		fmt.Printf("  [%s] %s -> %s\n", a.NodeID, a.Name, a.Ref)
	}
	return 0
}

func collectArtifactsFromActivations(activations []state.Activation) []artifactEntry {
	var results []artifactEntry
	for _, act := range activations {
		for key, val := range act.Outputs {
			inspectValue(act.ID, act.NodeID, key, val, &results)
		}
	}
	return results
}

func inspectValue(actID, nodeID, key string, val any, out *[]artifactEntry) {
	switch v := val.(type) {
	case string:
		if strings.HasPrefix(v, "cas://") || strings.HasPrefix(v, "s3://") {
			*out = append(*out, artifactEntry{
				ActivationID: actID,
				NodeID:       nodeID,
				Name:         key,
				Ref:          v,
			})
		}
	case map[string]any:
		if ref, ok := v["ref"].(string); ok && (strings.HasPrefix(ref, "cas://") || strings.HasPrefix(ref, "s3://")) {
			entry := artifactEntry{
				ActivationID: actID,
				NodeID:       nodeID,
				Name:         key,
				Ref:          ref,
			}
			if digest, ok := v["digest"].(string); ok {
				entry.Digest = digest
			}
			*out = append(*out, entry)
		} else {
			for subK, subV := range v {
				inspectValue(actID, nodeID, key+"."+subK, subV, out)
			}
		}
	}
}
