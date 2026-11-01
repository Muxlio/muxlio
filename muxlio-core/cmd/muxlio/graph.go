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
	"sort"
	"strings"
	"time"

	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
)

type graphNode struct {
	ID         string   `json:"id"`
	NodeID     string   `json:"nodeId"`
	Kind       string   `json:"kind,omitempty"`
	Executor   string   `json:"executor,omitempty"`
	State      string   `json:"state,omitempty"`
	Scope      string   `json:"scope,omitempty"`
	DependsOn  []string `json:"dependsOn,omitempty"`
	Occurrence int      `json:"occurrence,omitempty"`
}

type graphEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func cmdGraph(args []string, globalDataDir string, globalJSON bool) int {
	fs := flag.NewFlagSet("graph", flag.ContinueOnError)
	server := fs.String("server", os.Getenv("MUXLIO_SERVER"), "coordinator URL")
	format := fs.String("format", "ascii", "output format: ascii, json, mermaid")
	dataDir := fs.String("data-dir", globalDataDir, "runtime data directory (default: ~/.muxlio)")
	jsonOut := fs.Bool("json", globalJSON, "emit machine-readable JSON output")

	if err := fs.Parse(reorderArgs(args)); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: muxlio graph <plan.json|run-id> [--server URL] [--format ascii|json|mermaid] [--json]")
		return 2
	}

	target := fs.Arg(0)
	if *jsonOut {
		*format = "json"
	}

	// 1. Is target a local plan file?
	if strings.HasSuffix(target, ".json") || fileExists(target) {
		p, err := loadPlan(target)
		if err == nil && p != nil && len(p.Subgraphs) > 0 {
			return renderPlanGraph(p, *format)
		}
	}

	// 2. Otherwise treat target as a run ID
	runID := target
	// Try remote coordinator first if server specified
	if *server != "" {
		srvURL, token, err := resolveServer(*server, *dataDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 2
		}
		return fetchAndRenderRemoteGraph(srvURL, token, runID, *format)
	}

	// Try local database
	root := *dataDir
	if root == "" {
		root = defaultRuntime()
	}
	dbPath := filepath.Join(root, "data", "coordinator.db")
	if fileExists(dbPath) {
		store, err := openStore(root)
		if err == nil {
			defer store.Close()
			activations, err := store.ListActivations(runID)
			if err == nil && len(activations) > 0 {
				run, _ := store.GetRun(runID)
				return renderRunActivationsGraph(run, activations, *format)
			}
		}
	}

	// Fallback to default server
	srvURL, token, _ := resolveServer("", *dataDir)
	return fetchAndRenderRemoteGraph(srvURL, token, runID, *format)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func renderPlanGraph(p *plan.Plan, format string) int {
	nodes := make([]graphNode, 0)
	edges := make([]graphEdge, 0)

	// Sort subgraph names for deterministic output.
	sgNames := sortedSubgraphNames(p)

	for _, sgName := range sgNames {
		sg := p.Subgraphs[sgName]
		for _, n := range sg.Nodes {
			deps := extractNodeDependencies(&n)
			gn := graphNode{
				ID:        fmt.Sprintf("%s/%s", sgName, n.ID),
				NodeID:    n.ID,
				Kind:      n.Kind,
				Executor:  n.Executor,
				Scope:     sgName,
				DependsOn: deps,
			}
			nodes = append(nodes, gn)
			for _, dep := range deps {
				edges = append(edges, graphEdge{
					From: fmt.Sprintf("%s/%s", sgName, dep),
					To:   gn.ID,
				})
			}
		}
	}

	switch format {
	case "json":
		out, _ := json.MarshalIndent(map[string]any{
			"workflow":  p.Name,
			"irVersion": p.IRVersion,
			"digest":    p.Digest,
			"nodes":     nodes,
			"edges":     edges,
		}, "", "  ")
		fmt.Println(string(out))
		return 0

	case "mermaid":
		fmt.Println("```mermaid")
		fmt.Println("graph TD")
		for _, n := range nodes {
			label := fmt.Sprintf("%s<br/>(%s)", escapeMermaidLabel(n.NodeID), escapeMermaidLabel(n.Kind))
			if n.Executor != "" {
				label += fmt.Sprintf("<br/><i>%s</i>", escapeMermaidLabel(n.Executor))
			}
			fmt.Printf("    %s[\"%s\"]\n", sanitizeMermaidID(n.ID), label)
		}
		for _, e := range edges {
			fmt.Printf("    %s --> %s\n", sanitizeMermaidID(e.From), sanitizeMermaidID(e.To))
		}
		fmt.Println("```")
		return 0

	default: // ascii
		fmt.Printf("Plan Graph: %s (digest=%s)\n\n", p.Name, p.Digest)
		for _, sgName := range sgNames {
			sg := p.Subgraphs[sgName]
			fmt.Printf("Subgraph %q (%d nodes):\n", sgName, len(sg.Nodes))
			for i, n := range sg.Nodes {
				prefix := "├──"
				if i == len(sg.Nodes)-1 {
					prefix = "└──"
				}
				info := fmt.Sprintf("[%s] kind=%s", n.ID, n.Kind)
				if n.Executor != "" {
					info += fmt.Sprintf(" exec=%s", n.Executor)
				}
				deps := extractNodeDependencies(&n)
				if len(deps) > 0 {
					info += fmt.Sprintf(" deps=[%s]", strings.Join(deps, ", "))
				}
				fmt.Printf("  %s %s\n", prefix, info)
			}
			fmt.Println()
		}
		return 0
	}
}

// sortedSubgraphNames returns the subgraph names from p in sorted order so
// that all output formats are deterministic across invocations.
func sortedSubgraphNames(p *plan.Plan) []string {
	names := make([]string, 0, len(p.Subgraphs))
	for name := range p.Subgraphs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func extractNodeDependencies(n *plan.Node) []string {
	deps := make([]string, 0)
	seen := map[string]bool{}
	for _, b := range n.Inputs {
		if b.Ref != nil && b.Ref.Node != "" && !seen[b.Ref.Node] {
			seen[b.Ref.Node] = true
			deps = append(deps, b.Ref.Node)
		}
	}
	// Sort for deterministic output across map iteration orders.
	sort.Strings(deps)
	return deps
}

func renderRunActivationsGraph(run *state.Run, activations []state.Activation, format string) int {
	nodes := make([]graphNode, 0, len(activations))
	for _, a := range activations {
		scopeStr := "root"
		if len(a.ScopePath) > 0 {
			scopeStr = strings.Join(a.ScopePath, "/")
		}
		nodes = append(nodes, graphNode{
			ID:         a.ID,
			NodeID:     a.NodeID,
			State:      a.State,
			Scope:      scopeStr,
			Occurrence: a.Occurrence,
		})
	}

	switch format {
	case "json":
		res := map[string]any{
			"activations": nodes,
		}
		if run != nil {
			res["runId"] = run.ID
			res["workflow"] = run.WorkflowName
			res["state"] = run.State
		}
		out, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(out))
		return 0

	case "mermaid":
		fmt.Println("```mermaid")
		fmt.Println("graph TD")
		for _, n := range nodes {
			icon := stateIcon(n.State)
			label := fmt.Sprintf("%s %s<br/>(%s)", icon, escapeMermaidLabel(n.NodeID), escapeMermaidLabel(n.State))
			fmt.Printf("    %s[\"%s\"]\n", sanitizeMermaidID(n.ID), label)
		}
		fmt.Println("```")
		return 0

	default: // ascii
		runHdr := ""
		if run != nil {
			runHdr = fmt.Sprintf(" (Run: %s, State: %s)", run.ID, run.State)
		}
		fmt.Printf("Materialized Execution Graph%s:\n\n", runHdr)
		for i, n := range nodes {
			prefix := "├──"
			if i == len(nodes)-1 {
				prefix = "└──"
			}
			icon := stateIcon(n.State)
			fmt.Printf("  %s %s [%s] node=%s (scope=%s)\n", prefix, icon, n.State, n.NodeID, n.Scope)
		}
		return 0
	}
}

func fetchAndRenderRemoteGraph(serverURL, token, runID, format string) int {
	reqURL := strings.TrimRight(serverURL, "/") + "/v1/runs/" + url.PathEscape(runID) + "/graph"
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	setAuthHeader(req, token)
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error connecting to coordinator at %s: %v\n", serverURL, err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "HTTP %d: %s\n", resp.StatusCode, raw)
		return 1
	}

	var data struct {
		Activations []state.Activation `json:"activations"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		fmt.Fprintf(os.Stderr, "error parsing graph response: %v\n", err)
		return 1
	}
	return renderRunActivationsGraph(nil, data.Activations, format)
}

func stateIcon(st string) string {
	switch st {
	case "succeeded":
		return "[✓]"
	case "failed":
		return "[✗]"
	case "running":
		return "[⚡]"
	case "waiting":
		return "[⏳]"
	case "queued", "pending":
		return "[•]"
	case "cancelled":
		return "[⊘]"
	default:
		return "[?]"
	}
}

func sanitizeMermaidID(id string) string {
	// Replace path separators and common punctuation with underscores so the
	// result is a valid Mermaid identifier ([A-Za-z0-9_]+).
	// Double-quotes must also be stripped: if left in the identifier slot they
	// would break the surrounding `ID["label"]` syntax and corrupt the diagram.
	r := strings.NewReplacer("/", "_", "-", "_", ".", "_", ":", "_", " ", "_", `"`, "_")
	return r.Replace(id)
}

// escapeMermaidLabel escapes user-supplied text so it is safe to embed inside
// a double-quoted Mermaid node label (e.g. NodeA["<label here>"]).
//
// Mermaid renders quoted labels as HTML, so the following substitutions are
// applied to user data before it is spliced into a label string:
//
//   - `"` → `#quot;`  (Mermaid HTML-entity syntax; prevents closing the quoted label)
//   - `&` → `&amp;`
//   - `<` → `&lt;`
//   - `>` → `&gt;`
//   - `\n` / `\r` → a space   (newlines would break the single-line node declaration)
//
// This function must only be applied to user-originated values (NodeID, Kind,
// Executor…). Structural markup tokens such as `<br/>` and `<i>…</i>` are
// added by the caller after escaping and must not be passed through here.
func escapeMermaidLabel(s string) string {
	// Replace newlines first so the subsequent replacer never sees them.
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	r := strings.NewReplacer(
		`"`, `#quot;`,
		`&`, `&amp;`,
		`<`, `&lt;`,
		`>`, `&gt;`,
	)
	return r.Replace(s)
}
