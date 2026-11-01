package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
)

func runInspect(target string, dataDir string, jsonOut bool) int {
	// 1. Is target a local plan or typescript file that exists?
	if strings.HasSuffix(target, ".json") || strings.HasSuffix(target, ".ts") || fileExists(target) {
		p, err := loadPlan(target)
		if err == nil && p != nil {
			renderPlanInspect(p, jsonOut)
			return 0
		}
	}

	// 2. Otherwise treat target as a run ID
	runID := target
	// Try local database first
	root := dataDir
	if root == "" {
		root = defaultRuntime()
	}
	dbPath := filepath.Join(root, "data", "coordinator.db")
	if fileExists(dbPath) {
		store, err := openStore(root)
		if err == nil {
			defer store.Close()
			run, err := store.GetRun(runID)
			if err == nil && run != nil {
				return renderRunInspect(run, jsonOut)
			}
		}
	}

	// Try remote coordinator
	serverURL, token, err := resolveServer("", dataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	endpoint := fmt.Sprintf("%s/v1/runs/%s", strings.TrimRight(serverURL, "/"), url.PathEscape(runID))
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

	if jsonOut {
		fmt.Println(strings.TrimSpace(string(raw)))
		return 0
	}

	var run state.Run
	if err := json.Unmarshal(raw, &run); err != nil {
		fmt.Fprintf(os.Stderr, "error decoding run: %v\n", err)
		return 1
	}
	return renderRunInspect(&run, false)
}

func renderRunInspect(run *state.Run, jsonOut bool) int {
	if jsonOut {
		out, _ := json.MarshalIndent(run, "", "  ")
		fmt.Println(string(out))
		return 0
	}

	fmt.Printf("run: %s\n", run.ID)
	fmt.Printf("workflow: %s\n", run.WorkflowName)
	fmt.Printf("state: %s\n", run.State)
	if run.PlanDigest != "" {
		fmt.Printf("planDigest: %s\n", run.PlanDigest)
	}
	if !run.CreatedAt.IsZero() {
		fmt.Printf("createdAt: %s\n", run.CreatedAt.Format(time.RFC3339))
	}
	if !run.UpdatedAt.IsZero() {
		fmt.Printf("updatedAt: %s\n", run.UpdatedAt.Format(time.RFC3339))
	}
	if run.Error != "" {
		fmt.Printf("error: %s\n", run.Error)
	}
	if run.Output != nil {
		out, _ := json.MarshalIndent(run.Output, "", "  ")
		fmt.Printf("output:\n%s\n", string(out))
	}
	return 0
}

func renderPlanInspect(p *plan.Plan, jsonOut bool) {
	if jsonOut {
		out, _ := json.MarshalIndent(p, "", "  ")
		fmt.Println(string(out))
		return
	}
	fmt.Printf("workflow: %s\n", p.Name)
	fmt.Printf("irVersion: %s\n", p.IRVersion)
	fmt.Printf("digest: %s\n", p.Digest)
	fmt.Printf("root: %s\n", strOrDefault(p.Root, "root"))
	fmt.Printf("requiredExecutors: %s\n", strings.Join(p.RequiredExecutors, ", "))
	for name, sg := range p.Subgraphs {
		fmt.Printf("\nsubgraph %q (%d nodes):\n", name, len(sg.Nodes))
		for _, n := range sg.Nodes {
			fmt.Printf("  [%s] %s", n.ID, n.Kind)
			if n.Executor != "" {
				fmt.Printf(" executor=%s", n.Executor)
			}
			if n.BodyRef != "" {
				fmt.Printf(" body=%s", n.BodyRef)
			}
			fmt.Println()
		}
		if len(sg.Output) > 0 {
			ports := make([]string, 0, len(sg.Output))
			for port := range sg.Output {
				ports = append(ports, port)
			}
			fmt.Printf("  output ports: %s\n", strings.Join(ports, ", "))
		}
	}
}
