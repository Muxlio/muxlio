package api

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestPreflightIndependentNodeRequirements(t *testing.T) {
	store := newSQLiteStoreForPreflight(t)
	registerWorker(t, store, "one", []state.WorkerCapability{readyCapability("generic", "1")}, map[string]string{"pool": "one"})
	registerWorker(t, store, "two", []state.WorkerCapability{readyCapability("generic", "2")}, map[string]string{"pool": "two"})
	p := minimalPlan("generic")
	g := p.Subgraphs["root"]
	g.Nodes[0].ExecutorVersion = "1"
	g.Nodes[0].PlacementLabels = map[string]string{"pool": "one"}
	g.Nodes = append(g.Nodes, plan.Node{ID: "second", Kind: "execute", Executor: "generic", ExecutorVersion: "2", PlacementLabels: map[string]string{"pool": "two"}})
	p.Subgraphs["root"] = g
	resp := PreflightRun(&p, nil, store)
	if !resp.Feasible || len(resp.Executors) != 2 {
		t.Fatalf("independent workers should satisfy different requirements: %+v", resp)
	}
	for _, node := range resp.Executors {
		if len(node.MatchingWorkers) != 1 || len(node.NodeIDs) != 1 || node.Subgraph != "root" {
			t.Fatalf("missing node identity or eligible worker: %+v", node)
		}
	}
}

func TestPreflightNodeIdentityCannotCollide(t *testing.T) {
	store := newSQLiteStoreForPreflight(t)
	registerWorker(t, store, "worker", []state.WorkerCapability{readyCapability("generic", "")}, nil)
	p := minimalPlan("generic")
	p.Subgraphs["root"].Nodes[0].ID = "child/step"
	p.Subgraphs["root/child"] = plan.Subgraph{Nodes: []plan.Node{{ID: "step", Kind: "execute", Executor: "missing"}}}
	resp := PreflightRun(&p, nil, store)
	if len(resp.PlanIssues) != 0 {
		t.Fatalf("expected a valid plan: %+v", resp.PlanIssues)
	}
	if resp.Feasible || len(resp.Executors) != 2 {
		t.Fatalf("every distinct node must be checked: %+v", resp)
	}
	if !resp.Executors[0].Satisfiable || resp.Executors[1].Satisfiable {
		t.Fatalf("lost independent requirements: %+v", resp.Executors)
	}
}

func TestPreflightRequiresPinnablePlan(t *testing.T) {
	store := newSQLiteStoreForPreflight(t)
	registerWorker(t, store, "worker", []state.WorkerCapability{readyCapability("generic", "")}, nil)
	p := minimalPlan("generic")
	// Valid JSON, but the current plan canonicalizer cannot encode this number.
	p.Subgraphs["root"].Nodes[0].Params = json.RawMessage(`{"scale":1e400}`)
	resp := PreflightRun(&p, nil, store)
	if resp.Feasible {
		t.Fatalf("plan that submission cannot pin passed preflight: %+v", resp)
	}
	found := false
	for _, issue := range resp.PlanIssues {
		found = found || issue.Code == "MUXLIO_PLAN_DIGEST" && issue.Severity == "error"
	}
	if !found {
		t.Fatalf("missing digest diagnostic: %+v", resp.PlanIssues)
	}
	if p.Digest != "" {
		t.Fatal("preflight mutated caller plan")
	}
}

func TestPreflightPreservesImplicitContractRequirements(t *testing.T) {
	store := newSQLiteStoreForPreflight(t)
	capability := readyCapability("generic", "1")
	capability.Contract = &contract.AdapterManifest{ManifestVersion: "1", ContractVersion: "1", Executor: "generic", ExecutorVersion: "1"}
	registerWorker(t, store, "worker", []state.WorkerCapability{capability}, nil)
	for _, tc := range []struct {
		name string
		edit func(*plan.Node)
	}{
		{"contract version", func(n *plan.Node) { n.ContractVersion = "2" }},
		{"artifact output", func(n *plan.Node) { n.Outputs = map[string]plan.OutputDecl{"report": {Path: "report.txt"}} }},
		{"session", func(n *plan.Node) { n.Session = &contract.SessionRequest{Mode: "create"} }},
		{"idempotency", func(n *plan.Node) { n.Retry = &plan.RetryPolicy{Effect: "idempotency-key"} }},
		{"remote task", func(n *plan.Node) { n.RemoteTask = &contract.RemoteTaskReference{ID: "task"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := minimalPlan("generic")
			if resp := PreflightRun(&p, nil, store); !resp.Feasible {
				t.Fatalf("baseline should be feasible: %+v", resp)
			}
			tc.edit(&p.Subgraphs["root"].Nodes[0])
			resp := PreflightRun(&p, nil, store)
			if resp.Feasible || len(resp.PlanIssues) != 0 || len(resp.Executors) != 1 || resp.Executors[0].Satisfiable {
				t.Fatalf("unsupported contract passed preflight: %+v", resp)
			}
		})
	}
}

func TestCoordinatorPreflightRejectsUnauthorizedSelectors(t *testing.T) {
	store := newSQLiteStoreForPreflight(t)
	c := NewCoordinator(store)
	c.Workspaces = workers.WorkspaceRegistry{
		"project": {WorkerID: "owner", Path: "/worker/project"},
		"other":   {WorkerID: "owner", Path: "/worker/other"},
	}
	registerWorker(t, store, "owner", []state.WorkerCapability{readyCapability("generic", "")}, map[string]string{workers.WorkspaceIdentityLabelPrefix + "project": "true"})
	for _, tc := range []struct {
		name string
		edit func(*PreflightRequest)
	}{
		{"conflicting selectors", func(r *PreflightRequest) { r.Plan.WorkspaceID = "other" }},
		{"input path", func(r *PreflightRequest) { r.Input = json.RawMessage(`{"workspace":"/outside"}`) }},
		{"static node path", func(r *PreflightRequest) {
			r.Plan.Subgraphs["root"].Nodes[0].Inputs = map[string]plan.Binding{"workspace": {Expr: &plan.Expr{Op: "literal", Value: json.RawMessage(`"/outside"`)}}}
		}},
		{"digest mismatch", func(r *PreflightRequest) { r.Plan.Digest = "sha256:incorrect" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := PreflightRequest{WorkspaceID: "project", Plan: minimalPlan("generic")}
			tc.edit(&req)
			if resp := c.PreflightRun(req); resp.Feasible || resp.WorkspaceError == "" {
				t.Fatalf("unauthorized request passed preflight: %+v", resp)
			}
		})
	}
}

func TestPreflightPermissionAndIsolationContract(t *testing.T) {
	store := newSQLiteStoreForPreflight(t)
	registerWorker(t, store, "worker", []state.WorkerCapability{readyCapability("generic", "")}, map[string]string{workers.IsolationLabelPrefix + "bwrap": "true"})
	for _, tc := range []struct {
		name, params, mode string
		feasible           bool
	}{
		{"restricted native", `{"executionPolicy":{"trust":"restricted"}}`, "empty", false},
		{"isolated", `{"executionPolicy":{"trust":"restricted"},"isolation":{"mode":"required","runtime":"bwrap"}}`, "empty", true},
		{"missing runtime", `{"isolation":{"mode":"required"}}`, "empty", false},
		{"missing image", `{"isolation":{"mode":"required","runtime":"oci","enabled":true}}`, "empty", false},
		{"read only native", `{}`, "read-only", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := minimalPlan("generic")
			g := p.Subgraphs["root"]
			g.Nodes[0].Params, g.Nodes[0].WorkspaceMode = json.RawMessage(tc.params), tc.mode
			p.Subgraphs["root"] = g
			resp := PreflightRun(&p, nil, store)
			if resp.Feasible != tc.feasible {
				t.Fatalf("feasible=%v: %+v", resp.Feasible, resp)
			}
		})
	}
}

func TestCoordinatorPreflightWorkspaceAndProbeEvidence(t *testing.T) {
	store := newSQLiteStoreForPreflight(t)
	c := NewCoordinator(store)
	c.Workspaces = workers.WorkspaceRegistry{"project": {WorkerID: "owner", Path: "/worker/project", Aliases: []string{"alias"}}}
	capability := readyCapability("generic", "1")
	capability.Readiness = &contract.ProbeResult{Ready: false, Checks: []contract.ProbeCheck{{Name: contract.ProbeBinary, Message: "install the required executable"}}}
	registerWorker(t, store, "owner", []state.WorkerCapability{capability}, map[string]string{workers.WorkspaceIdentityLabelPrefix + "project": "true"})
	registerWorker(t, store, "other", []state.WorkerCapability{readyCapability("generic", "1")}, nil)
	p := minimalPlan("generic")
	resp := c.PreflightRun(PreflightRequest{WorkspaceID: "unknown", Plan: p})
	if resp.Feasible || resp.WorkspaceError == "" {
		t.Fatalf("unregistered project accepted: %+v", resp)
	}
	resp = c.PreflightRun(PreflightRequest{WorkspaceID: "alias", Plan: p})
	if resp.Feasible || resp.WorkspaceID != "project" || resp.WorkspaceWorkerID != "owner" {
		t.Fatalf("wrong workspace eligibility: %+v", resp)
	}
	if len(resp.Executors) != 1 || len(resp.Executors[0].WorkerEvidence) != 2 {
		t.Fatalf("probe evidence missing: %+v", resp)
	}
	if got := resp.Executors[0].WorkerEvidence[1].Capability.Readiness.Checks[0].Message; got != "install the required executable" {
		t.Fatalf("probe diagnostic lost: %q", got)
	}
	registerWorker(t, store, "owner", []state.WorkerCapability{readyCapability("generic", "1")}, map[string]string{workers.WorkspaceIdentityLabelPrefix + "project": "true"})
	resp = c.PreflightRun(PreflightRequest{WorkspaceID: "alias", Plan: p})
	if !resp.Feasible || len(resp.Executors[0].MatchingWorkers) != 1 || resp.Executors[0].MatchingWorkers[0] != "owner" || len(resp.Deferred) == 0 || len(resp.Unverified) == 0 {
		t.Fatalf("expected qualified feasibility: %+v", resp)
	}
	if p.WorkspaceID != "" || p.Digest != "" {
		t.Fatal("preflight mutated caller plan")
	}
}

type failedPreflightRegistry struct{ state.Store }

func (failedPreflightRegistry) ListWorkers(time.Time) ([]state.Worker, error) {
	return nil, errors.New("registry unavailable")
}

func TestPreflightUnavailableEvidence(t *testing.T) {
	p := minimalPlan("generic")
	p.InputSchema = json.RawMessage(`{"type":"object","required":["prompt"]}`)
	resp := PreflightRun(&p, nil, failedPreflightRegistry{state.New()})
	if resp.Feasible || resp.InputError == "" || resp.WorkerError != "registry unavailable" {
		t.Fatalf("missing evidence treated as success: %+v", resp)
	}
	for _, item := range resp.Verified {
		if item == "worker_registry_snapshot" || item == "executor_readiness_last_probe" {
			t.Fatalf("failed registry claimed verification: %v", resp.Verified)
		}
	}
}

func TestPreflightRejectsDrainingWorker(t *testing.T) {
	p := minimalPlan("generic")
	store := newSQLiteStoreForPreflight(t)
	registry := store.(dispatchStore)
	if err := registry.RegisterWorker(state.Worker{ID: "draining", Capacity: 1, Draining: true, LastSeen: time.Now(), Capabilities: []state.WorkerCapability{readyCapability("generic", "")}}); err != nil {
		t.Fatal(err)
	}
	if resp := PreflightRun(&p, nil, store); resp.Feasible {
		t.Fatalf("draining worker eligible: %+v", resp)
	}
}
