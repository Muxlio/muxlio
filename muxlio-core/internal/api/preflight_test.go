package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
)

// minimalPlan builds a structurally valid plan with one execute node using the
// given executor name.
func minimalPlan(executor string) plan.Plan {
	return plan.Plan{
		IRVersion: "1.0",
		Name:      "preflight-test",
		Root:      "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {
				Nodes: []plan.Node{
					{ID: "step", Kind: "execute", Executor: executor},
				},
			},
		},
	}
}

// registerWorker inserts a live worker with the given capabilities into a
// SQLite-backed store (or any state.Store that implements dispatchStore).
func registerWorker(t *testing.T, store state.Store, workerID string, caps []state.WorkerCapability, labels map[string]string) {
	t.Helper()
	ds, ok := store.(dispatchStore)
	if !ok {
		t.Fatal("store does not implement dispatchStore")
	}
	w := state.Worker{
		ID:           workerID,
		InstanceID:   workerID + "-inst",
		Capacity:     4,
		Capabilities: caps,
		Labels:       labels,
		LastSeen:     time.Now(),
	}
	if err := ds.RegisterWorker(w); err != nil {
		t.Fatalf("registerWorker: %v", err)
	}
}

// readyCapability builds a WorkerCapability with a passing readiness probe.
func readyCapability(name, version string) state.WorkerCapability {
	probe := contract.ProbeResult{
		Ready: true,
		Checks: []contract.ProbeCheck{
			{Name: contract.ProbeBinary, Passed: true, Message: "ok"},
		},
	}
	return state.WorkerCapability{Name: name, Version: version, Readiness: &probe}
}

// unreadyCapability builds a WorkerCapability whose readiness probe is failing.
func unreadyCapability(name string) state.WorkerCapability {
	probe := contract.ProbeResult{
		Ready: false,
		Checks: []contract.ProbeCheck{
			{Name: contract.ProbeAuthentication, Passed: false, Message: "not logged in"},
		},
	}
	return state.WorkerCapability{Name: name, Readiness: &probe}
}

// newSQLiteStoreForPreflight creates a SQLite store with real worker matching.
func newSQLiteStoreForPreflight(t *testing.T) state.Store {
	t.Helper()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "coordinator.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// ---------------------------------------------------------------------------
// Unit tests for PreflightRun
// ---------------------------------------------------------------------------

func TestPreflightRun_ValidPlanNoWorkers(t *testing.T) {
	p := minimalPlan("agent/test")
	store := state.New()

	resp := PreflightRun(&p, nil, store)

	// No plan errors; no workers ⇒ executor is not satisfiable.
	if len(resp.PlanIssues) != 0 {
		t.Fatalf("unexpected plan issues: %v", resp.PlanIssues)
	}
	if resp.InputError != "" {
		t.Fatalf("unexpected input error: %s", resp.InputError)
	}
	if resp.Feasible {
		t.Fatal("expected Feasible=false when no workers registered")
	}
	if len(resp.Executors) != 1 {
		t.Fatalf("expected 1 executor entry, got %d", len(resp.Executors))
	}
	ep := resp.Executors[0]
	if ep.Executor != "agent/test" {
		t.Fatalf("unexpected executor name: %s", ep.Executor)
	}
	if ep.Satisfiable {
		t.Fatal("executor should not be satisfiable with no workers")
	}
	if len(ep.BlockingReasons) == 0 {
		t.Fatal("expected at least one blocking reason")
	}
}

func TestPreflightRun_InvalidPlan(t *testing.T) {
	p := plan.Plan{} // missing irVersion, name, root
	store := state.New()

	resp := PreflightRun(&p, nil, store)

	if resp.Feasible {
		t.Fatal("expected Feasible=false for invalid plan")
	}
	// Should have structural errors.
	hasError := false
	for _, issue := range resp.PlanIssues {
		if issue.Severity == "error" {
			hasError = true
			break
		}
	}
	if !hasError {
		t.Fatalf("expected plan errors, got: %v", resp.PlanIssues)
	}
}

func TestPreflightRun_InputValidation(t *testing.T) {
	p := minimalPlan("agent/test")
	p.InputSchema = json.RawMessage(`{"type":"object","required":["name"],"properties":{"name":{"type":"string"}}}`)
	store := state.New()

	// Missing required field.
	badInput := json.RawMessage(`{}`)
	resp := PreflightRun(&p, badInput, store)
	if resp.InputError == "" {
		t.Fatal("expected InputError for invalid input")
	}
	if resp.Feasible {
		t.Fatal("expected Feasible=false when input is invalid")
	}

	// Valid input.
	goodInput := json.RawMessage(`{"name":"hello"}`)
	resp2 := PreflightRun(&p, goodInput, store)
	if resp2.InputError != "" {
		t.Fatalf("unexpected InputError for valid input: %s", resp2.InputError)
	}
}

func TestPreflightRun_WithMatchingWorker(t *testing.T) {
	p := minimalPlan("agent/test")
	store := newSQLiteStoreForPreflight(t)

	// Register a live worker with the right capability.
	registerWorker(t, store, "worker-1",
		[]state.WorkerCapability{readyCapability("agent/test", "1.0")},
		nil,
	)

	resp := PreflightRun(&p, nil, store)

	if !resp.Feasible {
		t.Fatalf("expected Feasible=true with matching worker; blocking: %v", resp.Executors)
	}
	if len(resp.Executors) != 1 {
		t.Fatalf("expected 1 executor, got %d", len(resp.Executors))
	}
	ep := resp.Executors[0]
	if !ep.Satisfiable {
		t.Fatalf("executor should be satisfiable; reasons: %v", ep.BlockingReasons)
	}
	if len(ep.MatchingWorkers) == 0 {
		t.Fatal("expected at least one matching worker")
	}
}

func TestPreflightRun_WorkerUnready(t *testing.T) {
	p := minimalPlan("agent/test")
	store := newSQLiteStoreForPreflight(t)

	registerWorker(t, store, "worker-unready",
		[]state.WorkerCapability{unreadyCapability("agent/test")},
		nil,
	)

	resp := PreflightRun(&p, nil, store)

	if resp.Feasible {
		t.Fatal("expected Feasible=false when worker is not ready")
	}
	ep := resp.Executors[0]
	if ep.Satisfiable {
		t.Fatal("executor should not be satisfiable when worker is unready")
	}
	found := false
	for _, r := range ep.BlockingReasons {
		if r == "executor_not_ready" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected executor_not_ready in blocking reasons; got %v", ep.BlockingReasons)
	}
}

func TestPreflightRun_ExecutorVersionMismatch(t *testing.T) {
	p := minimalPlan("agent/x")
	// Require version 2.0, but worker only has 1.0.
	p.Subgraphs["root"].Nodes[0] = plan.Node{
		ID:              "step",
		Kind:            "execute",
		Executor:        "agent/x",
		ExecutorVersion: "2.0",
	}
	store := newSQLiteStoreForPreflight(t)
	registerWorker(t, store, "worker-v1",
		[]state.WorkerCapability{readyCapability("agent/x", "1.0")},
		nil,
	)

	resp := PreflightRun(&p, nil, store)

	if resp.Feasible {
		t.Fatal("expected Feasible=false due to version mismatch")
	}
	ep := resp.Executors[0]
	if ep.Satisfiable {
		t.Fatal("executor should not be satisfiable with wrong version")
	}
	found := false
	for _, r := range ep.BlockingReasons {
		if r == "executor_version_mismatch" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected executor_version_mismatch; got %v", ep.BlockingReasons)
	}
	if ep.Constraints.ExecutorVersion != "2.0" {
		t.Fatalf("expected ExecutorVersion=2.0, got %q", ep.Constraints.ExecutorVersion)
	}
}

func TestPreflightRun_PlacementLabelMismatch(t *testing.T) {
	p := plan.Plan{
		IRVersion: "1.0",
		Name:      "placement-test",
		Root:      "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {
				Nodes: []plan.Node{
					{
						ID:              "step",
						Kind:            "execute",
						Executor:        "agent/y",
						PlacementLabels: map[string]string{"region": "us-east-1"},
					},
				},
			},
		},
	}
	store := newSQLiteStoreForPreflight(t)

	// Worker has no labels.
	registerWorker(t, store, "worker-no-label",
		[]state.WorkerCapability{readyCapability("agent/y", "")},
		nil,
	)

	resp := PreflightRun(&p, nil, store)
	if resp.Feasible {
		t.Fatal("expected Feasible=false with no matching placement label")
	}
	ep := resp.Executors[0]
	if ep.Satisfiable {
		t.Fatal("executor should not be satisfiable without placement label")
	}
	found := false
	for _, r := range ep.BlockingReasons {
		if r == "placement_label_mismatch" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected placement_label_mismatch; got %v", ep.BlockingReasons)
	}
}

func TestPreflightRun_PlacementLabelMatches(t *testing.T) {
	p := plan.Plan{
		IRVersion: "1.0",
		Name:      "placement-match",
		Root:      "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {
				Nodes: []plan.Node{
					{
						ID:              "step",
						Kind:            "execute",
						Executor:        "agent/y",
						PlacementLabels: map[string]string{"region": "us-east-1"},
					},
				},
			},
		},
	}
	store := newSQLiteStoreForPreflight(t)

	registerWorker(t, store, "worker-with-label",
		[]state.WorkerCapability{readyCapability("agent/y", "")},
		map[string]string{"region": "us-east-1"},
	)

	resp := PreflightRun(&p, nil, store)
	if !resp.Feasible {
		t.Fatalf("expected Feasible=true; blocking: %v", resp.Executors)
	}
}

func TestPreflightRun_IsolationRuntimeUnavailable(t *testing.T) {
	params, _ := json.Marshal(map[string]any{
		"isolation": map[string]any{"mode": "required", "runtime": "bwrap"},
	})
	p := plan.Plan{
		IRVersion: "1.0",
		Name:      "isolation-test",
		Root:      "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {
				Nodes: []plan.Node{
					{
						ID:       "step",
						Kind:     "execute",
						Executor: "agent/z",
						Params:   params,
					},
				},
			},
		},
	}
	store := newSQLiteStoreForPreflight(t)

	// Worker does not advertise the bwrap isolation label.
	registerWorker(t, store, "worker-no-iso",
		[]state.WorkerCapability{readyCapability("agent/z", "")},
		nil,
	)

	resp := PreflightRun(&p, nil, store)
	if resp.Feasible {
		t.Fatal("expected Feasible=false when isolation runtime unavailable")
	}
	ep := resp.Executors[0]
	found := false
	for _, r := range ep.BlockingReasons {
		if r == "isolation_runtime_unavailable" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected isolation_runtime_unavailable; got %v", ep.BlockingReasons)
	}
}

func TestPreflightRun_IsolationRuntimeAvailable(t *testing.T) {
	params, _ := json.Marshal(map[string]any{
		"isolation": map[string]any{"mode": "required", "runtime": "bwrap"},
	})
	p := plan.Plan{
		IRVersion: "1.0",
		Name:      "isolation-test",
		Root:      "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {
				Nodes: []plan.Node{
					{
						ID:       "step",
						Kind:     "execute",
						Executor: "agent/z",
						Params:   params,
					},
				},
			},
		},
	}
	store := newSQLiteStoreForPreflight(t)

	registerWorker(t, store, "worker-with-iso",
		[]state.WorkerCapability{readyCapability("agent/z", "")},
		map[string]string{"muxlio.dev/isolation/bwrap": "true"},
	)

	resp := PreflightRun(&p, nil, store)
	if !resp.Feasible {
		t.Fatalf("expected Feasible=true with isolation label; executors: %+v", resp.Executors)
	}
}

func TestPreflightRun_RequiredCapabilityMissing(t *testing.T) {
	p := plan.Plan{
		IRVersion: "1.0",
		Name:      "cap-test",
		Root:      "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {
				Nodes: []plan.Node{
					{
						ID:                   "step",
						Kind:                 "execute",
						Executor:             "agent/w",
						RequiredCapabilities: map[string]string{"artifacts": "1"},
					},
				},
			},
		},
	}
	store := newSQLiteStoreForPreflight(t)

	// Worker has the capability entry but no manifest with capabilities.
	registerWorker(t, store, "worker-no-cap",
		[]state.WorkerCapability{readyCapability("agent/w", "")},
		nil,
	)

	resp := PreflightRun(&p, nil, store)
	if resp.Feasible {
		t.Fatal("expected Feasible=false when required capability is not in manifest")
	}
	ep := resp.Executors[0]
	found := false
	for _, r := range ep.BlockingReasons {
		if r == "missing_required_capability" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected missing_required_capability; got %v", ep.BlockingReasons)
	}
}

func TestPreflightRun_RequiredCapabilityPresent(t *testing.T) {
	p := plan.Plan{
		IRVersion: "1.0",
		Name:      "cap-match",
		Root:      "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {
				Nodes: []plan.Node{
					{
						ID:                   "step",
						Kind:                 "execute",
						Executor:             "agent/w",
						RequiredCapabilities: map[string]string{"artifacts": "1"},
					},
				},
			},
		},
	}
	store := newSQLiteStoreForPreflight(t)

	manifest := &contract.AdapterManifest{
		ManifestVersion: "1",
		Executor:        "agent/w",
		Capabilities:    map[string]bool{"artifacts": true},
	}
	probe := contract.ProbeResult{
		Ready:  true,
		Checks: []contract.ProbeCheck{{Name: contract.ProbeBinary, Passed: true, Message: "ok"}},
	}
	registerWorker(t, store, "worker-with-cap",
		[]state.WorkerCapability{{Name: "agent/w", Contract: manifest, Readiness: &probe}},
		nil,
	)

	resp := PreflightRun(&p, nil, store)
	if !resp.Feasible {
		t.Fatalf("expected Feasible=true with matching capability; executors: %+v", resp.Executors)
	}
}

func TestPreflightRun_MultipleExecutors_PartialFailure(t *testing.T) {
	p := plan.Plan{
		IRVersion: "1.0",
		Name:      "multi-exec",
		Root:      "root",
		Subgraphs: map[string]plan.Subgraph{
			"root": {
				Nodes: []plan.Node{
					{ID: "step-a", Kind: "execute", Executor: "exec/a"},
					{ID: "step-b", Kind: "execute", Executor: "exec/b"},
				},
			},
		},
	}
	store := newSQLiteStoreForPreflight(t)

	// Register a worker for exec/a only.
	registerWorker(t, store, "worker-a",
		[]state.WorkerCapability{readyCapability("exec/a", "")},
		nil,
	)

	resp := PreflightRun(&p, nil, store)
	if resp.Feasible {
		t.Fatal("expected Feasible=false when exec/b has no worker")
	}
	satisfiable := make(map[string]bool)
	for _, ep := range resp.Executors {
		satisfiable[ep.Executor] = ep.Satisfiable
	}
	if !satisfiable["exec/a"] {
		t.Fatal("exec/a should be satisfiable")
	}
	if satisfiable["exec/b"] {
		t.Fatal("exec/b should not be satisfiable")
	}
}

func TestPreflightRun_VerifiedAndUnverified(t *testing.T) {
	p := minimalPlan("agent/test")
	store := state.New()

	resp := PreflightRun(&p, nil, store)

	if len(resp.Verified) == 0 {
		t.Fatal("expected non-empty Verified list")
	}
	if len(resp.Unverified) == 0 {
		t.Fatal("expected non-empty Unverified list")
	}
	// Plan validity should always be verified.
	found := false
	for _, v := range resp.Verified {
		if v == "plan_structural_validity" {
			found = true
		}
	}
	if !found {
		t.Fatalf("plan_structural_validity not in Verified: %v", resp.Verified)
	}
}

// ---------------------------------------------------------------------------
// HTTP handler tests
// ---------------------------------------------------------------------------

func TestPreflightHTTPEndpoint_RejectsGet(t *testing.T) {
	store := state.New()
	mux := NewRouter(store, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/runs/preflight", nil)
	resp := httptest.NewRecorder()
	mux.ServeHTTP(resp, req)

	if resp.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", resp.Code)
	}
}

func TestPreflightHTTPEndpointRequiresControlPlaneAuthorization(t *testing.T) {
	handler := WithAuth(NewRouter(state.New(), nil), "client-token", "worker-token",
		AccessGrant{Token: "run-token", RunID: "run", Permissions: []string{"read", "control"}})
	for _, tc := range []struct {
		token  string
		status int
	}{
		{"", http.StatusUnauthorized},
		{"worker-token", http.StatusForbidden},
		{"run-token", http.StatusForbidden},
		{"client-token", http.StatusOK},
	} {
		req := httptest.NewRequest(http.MethodPost, "/v1/runs/preflight", bytes.NewBufferString(`{"plan":{}}`))
		req.RemoteAddr = "203.0.113.1:1234"
		if tc.token != "" {
			req.Header.Set("Authorization", "Bearer "+tc.token)
		}
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		if resp.Code != tc.status {
			t.Fatalf("status=%d want=%d: %s", resp.Code, tc.status, resp.Body)
		}
	}
}

func TestPreflightHTTPEndpoint_InvalidJSON(t *testing.T) {
	store := state.New()
	mux := NewRouter(store, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/runs/preflight", bytes.NewBufferString("not-json"))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	mux.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.Code)
	}
}

func TestPreflightHTTPEndpoint_ValidPlanNoWorkers(t *testing.T) {
	store := state.New()
	mux := NewRouter(store, nil)

	body, _ := json.Marshal(map[string]any{
		"plan": minimalPlan("agent/test"),
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/runs/preflight", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	mux.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body: %s", resp.Code, resp.Body.String())
	}

	var result PreflightResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if result.Feasible {
		t.Fatal("expected Feasible=false with no workers")
	}
	if len(result.Executors) != 1 || result.Executors[0].Executor != "agent/test" {
		t.Fatalf("unexpected executors: %v", result.Executors)
	}
}

func TestPreflightHTTPEndpoint_ValidPlanWithMatchingWorker(t *testing.T) {
	store := newSQLiteStoreForPreflight(t)
	registerWorker(t, store, "w1",
		[]state.WorkerCapability{readyCapability("agent/test", "")},
		nil,
	)
	mux := NewRouter(store, nil)

	body, _ := json.Marshal(map[string]any{
		"plan": minimalPlan("agent/test"),
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/runs/preflight", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	mux.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body: %s", resp.Code, resp.Body.String())
	}

	var result PreflightResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !result.Feasible {
		t.Fatalf("expected Feasible=true; executors: %+v", result.Executors)
	}
}

func TestPreflightHTTPEndpoint_InvalidInput(t *testing.T) {
	p := minimalPlan("agent/test")
	p.InputSchema = json.RawMessage(`{"type":"object","required":["name"],"properties":{"name":{"type":"string"}}}`)

	store := newSQLiteStoreForPreflight(t)
	registerWorker(t, store, "w1",
		[]state.WorkerCapability{readyCapability("agent/test", "")},
		nil,
	)
	mux := NewRouter(store, nil)

	body, _ := json.Marshal(map[string]any{
		"plan":  p,
		"input": map[string]any{"wrong_field": 1},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/runs/preflight", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	mux.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.Code)
	}

	var result PreflightResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if result.Feasible {
		t.Fatal("expected Feasible=false with invalid input")
	}
	if result.InputError == "" {
		t.Fatal("expected InputError to be set")
	}
}

func TestPreflightHTTPEndpoint_NoPlan(t *testing.T) {
	store := state.New()
	mux := NewRouter(store, nil)

	body, _ := json.Marshal(map[string]any{})
	req := httptest.NewRequest(http.MethodPost, "/v1/runs/preflight", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	mux.ServeHTTP(resp, req)

	// An empty plan fails validation; still returns 200 with Feasible=false.
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.Code)
	}
	var result PreflightResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if result.Feasible {
		t.Fatal("expected Feasible=false for empty plan")
	}
}

func TestPreflightHTTPEndpoint_WorkersChecked(t *testing.T) {
	store := newSQLiteStoreForPreflight(t)
	registerWorker(t, store, "w1", []state.WorkerCapability{readyCapability("exec/a", "")}, nil)
	registerWorker(t, store, "w2", []state.WorkerCapability{readyCapability("exec/b", "")}, nil)
	mux := NewRouter(store, nil)

	body, _ := json.Marshal(map[string]any{
		"plan": minimalPlan("exec/a"),
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/runs/preflight", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	mux.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.Code)
	}
	var result PreflightResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if result.WorkersChecked != 2 {
		t.Fatalf("expected WorkersChecked=2, got %d", result.WorkersChecked)
	}
}
