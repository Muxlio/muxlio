package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/muxlio/muxlio-core/internal/api"
)

const maxPreflightResponseBytes = 32 << 20

const preflightUsage = `Usage:
  muxlio preflight <workflow.ts|plan.json> [input.json] --server <profile|URL> --workspace-id <id> [--json]

Check a workflow, input, and workspace against the coordinator's current worker
information without starting a run. Omitted input defaults to {}.
Authentication uses the server profile, --token <token>, --token-file <file>,
or MUXLIO_TOKEN, as with muxlio run --server.

Exit codes are 0 for feasible, 1 for infeasible or a failed check, and 2 for usage errors.
`

func cmdPreflight(args []string, dataDir string) int {
	var server, workspaceID, tokenFlag, tokenFile string
	jsonOut := false
	options := map[string]*string{
		"server": &server, "workspace-id": &workspaceID, "data-dir": &dataDir,
		"token": &tokenFlag, "token-file": &tokenFile,
	}
	var paths []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--help", "-h":
			fmt.Print(preflightUsage)
			return 0
		case "--json":
			jsonOut = true
			continue
		}
		if !strings.HasPrefix(arg, "-") {
			paths = append(paths, arg)
			continue
		}
		name := longFlagName(arg)
		dst := options[name]
		value, embedded := longFlagOptionalValue(arg)
		if dst == nil || !strings.HasPrefix(arg, "--") {
			fmt.Fprintf(os.Stderr, "error: unknown preflight option %q\n", arg)
			return 2
		}
		if !embedded {
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				fmt.Fprintf(os.Stderr, "error: --%s requires a value\n", name)
				return 2
			}
			i++
			value = args[i]
		}
		*dst = value
	}
	if len(paths) < 1 || len(paths) > 2 || strings.TrimSpace(server) == "" || strings.TrimSpace(workspaceID) == "" {
		fmt.Fprint(os.Stderr, preflightUsage)
		return 2
	}
	serverURL, token, err := resolveClientServer(server, dataDir, tokenFlag, tokenFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 2
	}
	p, err := loadPlan(paths[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: loading workflow %q: %v\n", paths[0], err)
		return 1
	}
	inputPath := ""
	if len(paths) == 2 {
		inputPath = paths[1]
	}
	input, err := readRunInput(inputPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: reading input: %v\n", err)
		return 1
	}
	if !json.Valid(input) {
		fmt.Fprintf(os.Stderr, "error: input file %q must contain a JSON value\n", inputPath)
		return 1
	}
	body, err := json.Marshal(api.PreflightRequest{WorkspaceID: workspaceID, Plan: *p, Input: input})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: encoding preflight request: %v\n", err)
		return 1
	}
	endpoint, err := url.JoinPath(serverURL, "v1/runs/preflight")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: resolving preflight endpoint: %v\n", err)
		return 2
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: creating preflight request: %v\n", err)
		return 1
	}
	req.Header.Set("Content-Type", "application/json")
	setAuthHeader(req, token)
	client := &http.Client{
		Timeout:       30 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: requesting preflight: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		fmt.Fprintf(os.Stderr, "error: coordinator returned HTTP %d: %s\n", resp.StatusCode, preflightTerminalText(string(bytes.TrimSpace(raw))))
		return 1
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxPreflightResponseBytes+1))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: reading preflight response: %v\n", err)
		return 1
	}
	if len(raw) > maxPreflightResponseBytes {
		fmt.Fprintf(os.Stderr, "error: preflight response exceeds %d MiB\n", maxPreflightResponseBytes>>20)
		return 1
	}
	var status struct {
		Feasible *bool `json:"feasible"`
	}
	if err := json.Unmarshal(raw, &status); err != nil || status.Feasible == nil {
		fmt.Fprintln(os.Stderr, "error: invalid coordinator preflight response (expected a JSON object with a boolean feasible field)")
		return 1
	}
	if jsonOut {
		// Forward unknown fields and exact numeric values along with the fields
		// understood by this version of the CLI.
		if _, err := fmt.Fprintln(os.Stdout, string(bytes.TrimSpace(raw))); err != nil {
			fmt.Fprintf(os.Stderr, "error: writing preflight response: %v\n", err)
			return 1
		}
	} else {
		var result api.PreflightResponse
		if err := json.Unmarshal(raw, &result); err != nil {
			fmt.Fprintf(os.Stderr, "error: decoding preflight diagnostics: %v\n", err)
			return 1
		}
		printPreflight(result, workspaceID)
	}
	if *status.Feasible {
		return 0
	}
	return 1
}

// Keep coordinator and workflow strings from inserting terminal controls or
// extra output lines. JSON diagnostics are forwarded as received.
func preflightTerminalText(value string) string {
	return strings.Map(func(r rune) rune {
		if !unicode.IsPrint(r) {
			return '�'
		}
		return r
	}, value)
}

func printPreflight(result api.PreflightResponse, workspaceID string) {
	if result.WorkspaceID != "" {
		workspaceID = result.WorkspaceID
	}
	status := "infeasible"
	if result.Feasible {
		status = "feasible"
	}
	fmt.Printf("Preflight %s for workspace %q", status, workspaceID)
	if result.WorkspaceWorkerID != "" {
		fmt.Printf(" (owner %q)", result.WorkspaceWorkerID)
	}
	fmt.Printf(" — %d live workers checked\n", result.WorkersChecked)
	if result.WorkspaceError != "" {
		fmt.Printf("  Workspace: %s\n", preflightTerminalText(result.WorkspaceError))
	}
	if result.WorkerError != "" {
		fmt.Printf("  Worker registry: %s\n", preflightTerminalText(result.WorkerError))
	}
	for _, issue := range result.PlanIssues {
		location := strings.Trim(issue.Subgraph+"/"+issue.NodeID, "/")
		if location != "" {
			location = " (" + location + ")"
		}
		line := fmt.Sprintf("%s %s%s: %s", issue.Severity, issue.Code, location, issue.Message)
		fmt.Printf("  Plan %s\n", preflightTerminalText(line))
		if issue.Fix != "" {
			fmt.Printf("    %s\n", preflightTerminalText(issue.Fix))
		}
	}
	if result.InputError != "" {
		fmt.Printf("  Input schema: %s\n", preflightTerminalText(result.InputError))
	}
	for _, executor := range result.Executors {
		if executor.Satisfiable {
			continue
		}
		line := fmt.Sprintf("%q (%s, nodes %s): %s", executor.Executor, executor.Subgraph, strings.Join(executor.NodeIDs, ", "), strings.Join(executor.BlockingReasons, ", "))
		fmt.Printf("  Executor %s\n", preflightTerminalText(line))
		for _, message := range executor.Diagnostics {
			fmt.Printf("    %s\n", preflightTerminalText(message))
		}
	}
	if len(result.Verified) > 0 {
		fmt.Printf("  Checked: %s\n", preflightTerminalText(strings.Join(result.Verified, ", ")))
	}
	if len(result.Unverified) > 0 {
		fmt.Printf("  Unverified: %s\n", preflightTerminalText(strings.Join(result.Unverified, ", ")))
	}
	if len(result.Deferred) > 0 {
		fmt.Printf("  Deferred: %s\n", preflightTerminalText(strings.Join(result.Deferred, ", ")))
	}
}
