package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/api"
	"github.com/muxlio/muxlio-core/internal/mcp"
	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestPreflightCLIWorkspace(t *testing.T) {
	dir := t.TempDir()
	localPath, err := workers.CanonicalWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(filepath.Join(dir, "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	coordinator := api.NewCoordinator(store)
	revision := "sha256:" + strings.Repeat("a", 64)
	otherRevision := "sha256:" + strings.Repeat("b", 64)
	coordinator.Workspaces = workers.WorkspaceRegistry{
		"first":    {WorkerID: "owner", Path: dir, Aliases: []string{"alias"}},
		"second":   {WorkerID: "other", Path: dir},
		"prepared": {WorkerID: "owner", Path: dir, Digest: revision, Aliases: []string{"prepared-alias"}},
		"stale":    {WorkerID: "owner", Path: dir, Digest: revision},
		"wrong":    {WorkerID: "owner", Path: dir},
		"offline":  {WorkerID: "absent", Path: dir},
		"expired":  {WorkerID: "expired-owner", Path: dir},
		"local":    {WorkerID: "local", Path: localPath},
	}
	register := func(id string, labels map[string]string) {
		t.Helper()
		if _, err := coordinator.Register(api.WorkerRegistration{
			WorkerID: id, Capacity: 1, Labels: labels,
			Capabilities: []api.WorkerCapability{{Name: "test"}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	register("owner", map[string]string{
		workers.WorkspaceIdentityLabelPrefix + "first":    "true",
		workers.WorkspaceIdentityLabelPrefix + "prepared": "true",
		workers.WorkspaceDigestLabelPrefix + "prepared":   revision,
		workers.WorkspaceIdentityLabelPrefix + "stale":    "true",
		workers.WorkspaceDigestLabelPrefix + "stale":      otherRevision,
	})
	register("other", map[string]string{
		workers.WorkspaceIdentityLabelPrefix + "second": "true",
		workers.WorkspaceIdentityLabelPrefix + "wrong":  "true",
	})
	if err := store.RegisterWorker(state.Worker{
		ID: "expired-owner", Capacity: 1, LastSeen: time.Now().Add(-time.Hour),
		Labels:       map[string]string{workers.WorkspaceIdentityLabelPrefix + "expired": "true"},
		Capabilities: []state.WorkerCapability{{Name: "test"}},
	}); err != nil {
		t.Fatal(err)
	}
	before, err := store.ListWorkers(time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	runner := &serverRunner{store: store, exec: coordinator, ctx: context.Background(), engines: map[string]*plan.Engine{}, workers: map[string]chan struct{}{}}
	router := api.WithAuth(api.NewRouter(store, runner), "client", "worker")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/runs/preflight" {
			t.Errorf("unexpected coordinator request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		router.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	mcpHandler, err := mcp.NewHTTPHandler(server.URL, "client", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, contents string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		assertWorkspaceFileUnchanged(t, path, contents)
		return path
	}
	config := write("config.json", fmt.Sprintf(`{"servers":{"test-profile":{"url":%q,"token":"client"}}}`, server.URL))
	t.Setenv("MUXLIO_HOME", dir)
	t.Setenv("MUXLIO_CONFIG", config)
	t.Setenv("MUXLIO_TOKEN", "")
	t.Setenv("MUXLIO_SERVER", "")
	token := write("token", "client\n")
	const input = `{"value":9007199254740993}`
	inputPath := write("input.json", input)
	writePlan := func(name string, edit func(*plan.Plan)) (string, json.RawMessage) {
		t.Helper()
		var p plan.Plan
		if err := json.Unmarshal([]byte(workspaceRunPlan), &p); err != nil {
			t.Fatal(err)
		}
		edit(&p)
		if _, err := p.ComputeDigest(); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		return write(name+".json", string(raw)), raw
	}
	baseline, definition := writePlan("plan", func(*plan.Plan) {})
	bound, _ := writePlan("bound", func(p *plan.Plan) { p.WorkspaceID = "first" })
	pinned, _ := writePlan("pinned", func(p *plan.Plan) { p.WorkspaceDigest = revision })
	unsupported, _ := writePlan("unsupported", func(p *plan.Plan) {
		p.Subgraphs["root"].Nodes[0].RequiredCapabilities = map[string]string{"artifacts": "1"}
	})
	pinnedUnsupported, _ := writePlan("pinned-unsupported", func(p *plan.Plan) {
		p.WorkspaceDigest = revision
		p.Subgraphs["root"].Nodes[0].RequiredCapabilities = map[string]string{"artifacts": "1"}
	})
	wrongVersion := write("wrong-version.json", strings.Replace(workspaceRunPlan, `"1.0"`, `"2.0"`, 1))
	for _, tc := range []struct {
		name, path, selector, canonical, message string
		omitInput                                bool
	}{
		{name: "alias", path: bound, selector: "alias", canonical: "first"},
		{name: "second", path: baseline, selector: "second", canonical: "second"},
		{name: "pinned digest", path: pinned, selector: "prepared-alias", canonical: "prepared"},
		{name: "unknown workspace", path: bound, selector: "unknown", message: `workspace "unknown" is not authorized`},
		{name: "plan mismatch", path: bound, selector: "second", message: "submission and plan workspace IDs disagree"},
		{name: "wrong owner", path: baseline, selector: "wrong", message: `owning worker "owner" does not advertise workspace "wrong"`},
		{name: "missing owner", path: baseline, selector: "offline", message: `no available owning worker "absent"`},
		{name: "owner heartbeat expired", path: baseline, selector: "expired", message: `no available owning worker "expired-owner"`},
		{name: "local owner offline", path: baseline, selector: "local", message: "workspace_owner_offline"},
		{name: "missing digest pin", path: baseline, selector: "prepared-alias", message: "prepared digest does not match the plan"},
		{name: "worker digest mismatch", path: pinned, selector: "stale", message: "advertises a mismatched digest"},
		{name: "unsupported capability", path: unsupported, selector: "first", message: "missing_required_capability"},
		{name: "pinned unsupported capability", path: pinnedUnsupported, selector: "prepared-alias", message: "missing_required_capability"},
		{name: "invalid input schema", path: baseline, selector: "first", omitInput: true, message: "Input schema"},
		{name: "invalid plan", path: wrongVersion, selector: "first", message: "MUXLIO_UNSUPPORTED_IR_VERSION"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			feasible := tc.message == ""
			exit := 0
			if !feasible {
				exit = 1
			}
			args := []string{"preflight", tc.path}
			effectiveInput := "{}"
			if !tc.omitInput {
				args = append(args, inputPath)
				effectiveInput = input
			}
			args = append(args, "--server", "test-profile", "--workspace-id="+tc.selector)
			raw := workspaceCLI(t, exit, append(args, "--json")...)
			var response api.PreflightResponse
			if err := json.Unmarshal([]byte(raw), &response); err != nil || response.Feasible != feasible {
				t.Fatalf("preflight JSON: %s (%v)", raw, err)
			}
			planJSON, err := os.ReadFile(tc.path)
			if err != nil {
				t.Fatal(err)
			}
			body := fmt.Sprintf(`{"workspaceId":%q,"plan":%s,"input":%s}`, tc.selector, planJSON, effectiveInput)
			req := httptest.NewRequest(http.MethodPost, "/v1/runs/preflight", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer client")
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusOK || raw != recorder.Body.String() {
				t.Fatalf("CLI response differs from HTTP %d\nCLI: %s\nAPI: %s", recorder.Code, raw, recorder.Body)
			}
			if feasible && (response.WorkspaceID != tc.canonical || len(response.Executors) != 1 ||
				len(response.Executors[0].WorkerEvidence) != 2 || len(response.Unverified) == 0) {
				t.Fatalf("missing canonical workspace or worker evidence: %s", raw)
			}
			for _, executor := range response.Executors {
				if response.WorkspaceError != "" && executor.Constraints.WorkspaceRequired &&
					(executor.Satisfiable || len(executor.MatchingWorkers) != 0 || len(executor.WorkerEvidence) == 0) {
					t.Fatalf("invalid workspace must retain evidence without eligible workers: %s", raw)
				}
				if response.WorkspaceError == "" && (slices.Contains(executor.BlockingReasons, "workspace_revision_mismatch") ||
					slices.Contains(executor.BlockingReasons, "workspace_owner_mismatch")) {
					t.Fatalf("irrelevant worker reported as a workspace blocker: %s", raw)
				}
			}
			assertMCPPreflightDecision(t, mcpHandler, body, feasible)
			human := workspaceCLI(t, exit, args...)
			if feasible {
				if !strings.Contains(human, fmt.Sprintf("Preflight feasible for workspace %q", tc.canonical)) ||
					!strings.Contains(human, "Unverified:") || !strings.Contains(human, "remote_quota_and_rate_limits") {
					t.Fatalf("human result: %s", human)
				}
			} else if !strings.Contains(human, "Preflight infeasible") || !strings.Contains(human, tc.message) {
				t.Fatalf("human result missing %q: %s", tc.message, human)
			}
		})
	}
	optional, _ := writePlan("optional-input", func(p *plan.Plan) {
		p.InputSchema = json.RawMessage(`{"type":"object","maxProperties":0}`)
	})
	workspaceCLI(t, 0, "--server", server.URL, "--workspace-id", "first", "preflight", optional, "--token-file", token, "--json")
	for _, target := range [][]string{{"--server", server.URL}, {"--server", "test-profile", "--token", "wrong"}} {
		out := workspaceCLI(t, 1, append([]string{"preflight", baseline, "--workspace-id", "first", "--json"}, target...)...)
		if !strings.Contains(out, "HTTP 401") {
			t.Fatalf("authentication failure: %s", out)
		}
	}
	invalidPlan := write("bad.json", "{")
	if out := workspaceCLI(t, 1, "preflight", invalidPlan, "--server", "test-profile", "--workspace-id", "first"); !strings.Contains(out, "parsing plan JSON") {
		t.Fatalf("malformed workflow: %s", out)
	}
	invalidInput := write("bad-input.json", "{")
	if out := workspaceCLI(t, 1, "preflight", baseline, invalidInput, "--server", "test-profile", "--workspace-id", "first"); !strings.Contains(out, "must contain a JSON value") {
		t.Fatalf("malformed input: %s", out)
	}
	t.Run("portable TypeScript", func(t *testing.T) {
		if _, err := exec.LookPath("node"); err != nil {
			t.Skip("node unavailable")
		}
		modules, err := filepath.Abs("../../../node_modules")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(modules, "tsx", "dist", "cli.mjs")); os.IsNotExist(err) {
			t.Skip("install the repository's pinned tsx to run the TypeScript integration")
		} else if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(modules, filepath.Join(dir, "node_modules")); err != nil {
			t.Fatal(err)
		}
		write("definition.ts", fmt.Sprintf("export const definition = %s;\n", definition))
		source := write("arbitrary.ts", "import { definition } from './definition.ts';\nexport default definition;\n")
		for _, workspace := range []string{"first", "second"} {
			out := workspaceCLI(t, 0, "preflight", source, inputPath, "--server=test-profile", "--workspace-id", workspace, "--json")
			var response api.PreflightResponse
			if err := json.Unmarshal([]byte(out), &response); err != nil || !response.Feasible || response.WorkspaceID != workspace {
				t.Fatalf("compiled source preflight: %s (%v)", out, err)
			}
		}
		malformedSource := write("malformed.ts", "export default {")
		if out := workspaceCLI(t, 1, "preflight", malformedSource, "--server", "test-profile", "--workspace-id", "first"); !strings.Contains(out, "could not compile workflow") {
			t.Fatalf("malformed TypeScript: %s", out)
		}
	})
	if runs, err := store.ListRuns(); err != nil || len(runs) != 0 || len(runner.engines) != 0 || len(runner.workers) != 0 {
		t.Fatalf("preflight created run state: %v (%v)", runs, err)
	}
	if versions, err := store.ListWorkflowVersions(); err != nil || len(versions) != 0 {
		t.Fatalf("preflight registered workflow versions: %v (%v)", versions, err)
	}
	if after, err := store.ListWorkers(time.Time{}); err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("preflight modified workers: before=%+v after=%+v (%v)", before, after, err)
	}
}

func assertMCPPreflightDecision(t *testing.T, handler http.Handler, args string, feasible bool) {
	t.Helper()
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"preflight_run","arguments":%s}}`, args)
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer client")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2025-11-25")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	var response struct {
		Result struct {
			IsError bool
			Content []struct{ Text string }
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != http.StatusOK ||
		response.Result.IsError == feasible || len(response.Result.Content) != 1 {
		t.Fatalf("MCP preflight: %d %s (%v)", w.Code, w.Body, err)
	}
	var result api.PreflightResponse
	if err := json.Unmarshal([]byte(response.Result.Content[0].Text), &result); err != nil || result.Feasible != feasible {
		t.Fatalf("MCP decision: %s (%v)", response.Result.Content[0].Text, err)
	}
}

func TestPreflightCLIUsageAndTransport(t *testing.T) {
	t.Setenv("MUXLIO_TOKEN", "client")
	t.Setenv("MUXLIO_SERVER", "unused")
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(path, []byte(workspaceRunPlan), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"preflight", path, "--workspace-id", "first"},
		{"preflight", path, "--server", "unused"},
		{"preflight", path, "--server", "unused", "--workspace-id", "--json"},
		{"--workspace-id=", "preflight", path, "--server", "unused"},
		{"preflight", path, "input.json", "extra", "--server", "unused", "--workspace-id", "first"},
		{"preflight", path, "--server", "unused", "--workspace-id", "first", "--fake-config", "fake.json"},
	} {
		workspaceCLI(t, 2, args...)
	}
	help := workspaceCLI(t, 0, "preflight", "--help")
	for _, option := range []string{"--server", "--workspace-id", "--json", "--token-file"} {
		if !strings.Contains(help, option) {
			t.Errorf("help is missing %q", option)
		}
	}
	const futureResponse = `{"feasible":false,"executors":[{"diagnostics":["unavailable"],"workerEvidence":[{"future":{"id":9007199254740993}}]}],"future":null}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request api.PreflightRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || r.Method != http.MethodPost ||
			r.URL.Path != "/v1/runs/preflight" || r.Header.Get("Authorization") != "Bearer client" ||
			request.WorkspaceID != "first" || !bytes.Equal(request.Input, []byte("{}")) {
			t.Errorf("unexpected request: %s %s %+v (%v)", r.Method, r.URL.Path, request, err)
		}
		fmt.Fprintln(w, futureResponse)
	}))
	t.Cleanup(server.Close)
	args := []string{"preflight", path, "--server", server.URL + "#fragment", "--workspace-id", "first", "--json"}
	if out := workspaceCLI(t, 1, args...); out != futureResponse+"\n" {
		t.Fatalf("JSON response changed: %s", out)
	}
	server.Close()
	if out := workspaceCLI(t, 1, args...); !strings.Contains(out, "requesting preflight") {
		t.Fatalf("network failure: %s", out)
	}
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/runs/preflight" {
			t.Errorf("preflight followed a redirect to %s", r.URL.Path)
			w.WriteHeader(http.StatusCreated)
			return
		}
		http.Redirect(w, r, "/v1/runs", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirect.Close)
	if out := workspaceCLI(t, 1, "preflight", path, "--server", redirect.URL, "--workspace-id", "first"); !strings.Contains(out, "HTTP 307") {
		t.Fatalf("redirect response: %s", out)
	}
	t.Run("unsafe terminal diagnostics", func(t *testing.T) {
		const response = `{"feasible":false,"workspaceError":"problem\u001b[2J\nPreflight feasible"}`
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintln(w, response)
		}))
		t.Cleanup(server.Close)
		args := []string{"preflight", path, "--server", server.URL, "--workspace-id", "first"}
		if out := workspaceCLI(t, 1, args...); strings.Contains(out, "\x1b") || strings.Contains(out, "\nPreflight feasible") {
			t.Fatalf("unsafe human output: %q", out)
		}
		if out := workspaceCLI(t, 1, append(args, "--json")...); out != response+"\n" {
			t.Fatalf("JSON diagnostics changed: %q", out)
		}
	})
	t.Run("oversized response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"feasible":true}`)
			padding := strings.Repeat(" ", 1<<20)
			for i := 0; i < maxPreflightResponseBytes>>20; i++ {
				if _, err := fmt.Fprint(w, padding); err != nil {
					return
				}
			}
		}))
		t.Cleanup(server.Close)
		out := workspaceCLI(t, 1, "preflight", path, "--server", server.URL, "--workspace-id", "first", "--json")
		if !strings.Contains(out, "preflight response exceeds") || strings.Contains(out, `"feasible"`) {
			t.Fatalf("oversized response should fail without partial JSON: %q", out)
		}
	})
}
