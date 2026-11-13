package mcp

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func discoveryWorker(id string, manifest *contract.AdapterManifest) state.Worker {
	return state.Worker{ID: id, Capacity: 1, Capabilities: []state.WorkerCapability{{Name: "generic/task", Version: "1", Contract: manifest, Readiness: &contract.ProbeResult{Ready: true, Checks: []contract.ProbeCheck{{Name: "binary", Passed: true}}}}}}
}

func TestExecutorCatalogPreservesVariantsAndDispatchEligibility(t *testing.T) {
	withSession := &contract.AdapterManifest{Executor: "generic/task", Capabilities: map[string]bool{"session": true}, CapabilityVersions: map[string]string{"session": "2"}}
	a, b := discoveryWorker("a", withSession), discoveryWorker("b", nil)
	c := discoveryWorker("c", withSession)
	c.Draining = true
	req := workers.ExecuteRequest{Session: &contract.SessionRequest{Mode: "create"}}
	choices := buildExecutorCatalog([]state.Worker{c, b, a}, []state.Assignment{{WorkerID: "a", State: "active"}}, "generic/task", req, true)
	choice := choices[0]
	if len(choice.Variants) != 2 || choice.EligibleWorkerCount != 1 || choice.AvailableWorkerCount != 0 || choice.QueueReason != "worker_full" {
		t.Fatalf("choice: %+v", choice)
	}
	if choice.Workers[0].Reason.Code != "worker_full" || choice.Workers[1].Reason.Code != "missing_required_capability" || choice.Workers[2].Reason.Code != "worker_draining" {
		t.Fatalf("evidence: %+v", choice.Workers)
	}
	req.Executor = "generic/task"
	raw, _ := json.Marshal(req)
	for i, w := range []state.Worker{a, b} {
		if choice.Workers[i].Eligible != workers.MatchWorkerDispatch(w.ID, req.Executor, raw, w.Capabilities, w.Labels) {
			t.Fatalf("eligibility differs from dispatch: %s", w.ID)
		}
	}
}

func TestExecutorCatalogConstraints(t *testing.T) {
	base := discoveryWorker("worker", &contract.AdapterManifest{ProbeVersion: "1"})
	cases := []struct {
		name   string
		change func(*state.Worker, *workers.ExecuteRequest)
		reason string
	}{
		{"readiness", func(w *state.Worker, r *workers.ExecuteRequest) { w.Capabilities[0].Readiness = nil }, "executor_not_ready"},
		{"version", func(w *state.Worker, r *workers.ExecuteRequest) { r.ExecutorVersion = "2" }, "executor_version_mismatch"},
		{"placement", func(w *state.Worker, r *workers.ExecuteRequest) {
			r.PlacementLabels = map[string]string{"region": "elsewhere"}
		}, "placement_label_mismatch"},
		{"isolation", func(w *state.Worker, r *workers.ExecuteRequest) {
			r.Params = json.RawMessage(`{"isolation":{"mode":"required","runtime":"oci","image":"test"}}`)
		}, "isolation_runtime_unavailable"},
		{"workspace", func(w *state.Worker, r *workers.ExecuteRequest) {
			r.WorkspaceID = "workspace"
			r.WorkspaceWorkerID = "other"
		}, "workspace_owner_mismatch"},
		{"capability", func(w *state.Worker, r *workers.ExecuteRequest) {
			r.RequiredCapabilities = map[string]string{"usage": "1"}
		}, "missing_required_capability"},
		{"capacity", func(w *state.Worker, r *workers.ExecuteRequest) { w.Capacity = 0 }, "worker_capacity_disabled"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			raw, _ := json.Marshal(base)
			var w state.Worker
			_ = json.Unmarshal(raw, &w)
			req := workers.ExecuteRequest{}
			tt.change(&w, &req)
			c := buildExecutorCatalog([]state.Worker{w}, nil, "generic/task", req, true)[0]
			if c.EligibleWorkerCount != 0 || len(c.Workers) != 1 || c.Workers[0].Reason.Code != tt.reason {
				t.Fatalf("choice: %+v evidence: %+v", c, c.Workers)
			}
		})
	}
}

func TestExecutorDiscoveryTools(t *testing.T) {
	w := discoveryWorker("worker", nil)
	ts := newTestServer(t, http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/v1/workers" {
			t.Errorf("unexpected call: %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(rw).Encode(map[string]any{"workers": []state.Assignment{}, "registeredWorkers": []state.Worker{w}})
	}))
	srv := &server{api: newAPIClient(ts.URL)}
	for _, name := range []string{"list_executors", "explain_capability"} {
		result := srv.dispatchTool(name, json.RawMessage(`{"executor":"generic/task","request":{"requiredCapabilities":{"remoteTask":"1"}}}`))
		if result.IsError || !strings.Contains(result.Content[0].Text, "missing_required_capability") {
			t.Fatalf("%s: %+v", name, result)
		}
		var response struct {
			CapacityEstimate struct {
				Basis                     string `json:"basis"`
				AssumedSlotsPerAssignment int    `json:"assumedSlotsPerAssignment"`
				WeightedAdmissionVerified *bool  `json:"weightedAdmissionVerified"`
			} `json:"capacityEstimate"`
			Unverified []string `json:"unverified"`
		}
		if err := json.Unmarshal([]byte(result.Content[0].Text), &response); err != nil {
			t.Fatal(err)
		}
		estimate := response.CapacityEstimate
		if estimate.Basis != "active_assignment_count" || estimate.AssumedSlotsPerAssignment != 1 || estimate.WeightedAdmissionVerified == nil || *estimate.WeightedAdmissionVerified {
			t.Fatalf("%s: missing capacity estimate provenance: %+v", name, estimate)
		}
		if !strings.Contains(strings.Join(response.Unverified, ","), "weighted_worker_capacity") {
			t.Fatalf("%s: weighted capacity must be unverified: %+v", name, response.Unverified)
		}
	}
	for _, args := range []string{`{}`, `null`, `{"executor":"generic/task","request":null}`, `{"executor":""}`, `{"executor":"generic/task","typo":true}`, `{"executor":"generic/task","request":{"typo":true}}`, `{"executor":"generic/task"} {}`} {
		if !srv.dispatchTool("explain_capability", json.RawMessage(args)).IsError {
			t.Errorf("accepted invalid args: %s", args)
		}
	}
	result := srv.dispatchTool("explain_capability", json.RawMessage(`{"executor":"missing"}`))
	if result.IsError || !strings.Contains(result.Content[0].Text, "executor_not_registered") {
		t.Fatalf("unknown executor: %+v", result)
	}
}

func TestExecutorCatalogEmptyAndIsolationEvidence(t *testing.T) {
	if choices := buildExecutorCatalog(nil, nil, "", workers.ExecuteRequest{}, false); len(choices) != 0 || choices == nil {
		t.Fatalf("empty catalog: %#v", choices)
	}
	w := discoveryWorker("worker", nil)
	w.Labels = map[string]string{workers.IsolationLabelPrefix + "oci": "true"}
	c := buildExecutorCatalog([]state.Worker{w}, nil, "", workers.ExecuteRequest{}, false)[0]
	if c.AvailableWorkerCount != 1 || c.QueueReason != "" || c.Variants[0].IsolationWorkerCounts["oci"] != 1 || len(c.Workers) != 0 {
		t.Fatalf("catalog: %+v", c)
	}
}

// Neither worker can satisfy the full request, even though their descriptors
// collectively advertise every required feature.
func TestExecutorCatalogDoesNotUnionCapabilitiesAcrossWorkers(t *testing.T) {
	a := discoveryWorker("a", &contract.AdapterManifest{Capabilities: map[string]bool{contract.CapabilitySession: true}, CapabilityVersions: map[string]string{contract.CapabilitySession: contract.SessionVersion}})
	b := discoveryWorker("b", &contract.AdapterManifest{Capabilities: map[string]bool{contract.CapabilityUsage: true, contract.CapabilitySession: false}})
	req := workers.ExecuteRequest{RequiredCapabilities: map[string]string{contract.CapabilitySession: contract.SessionVersion, contract.CapabilityUsage: contract.Version}}
	c := buildExecutorCatalog([]state.Worker{a, b}, nil, "", req, true)[0]
	if c.EligibleWorkerCount != 0 || c.AvailableWorkerCount != 0 || c.QueueReason != "no_eligible_workers" || len(c.Variants) != 2 {
		t.Fatalf("combined incompatible installations: %+v", c)
	}
	for _, v := range c.Variants {
		if v.EligibleWorkerCount != 0 || len(v.CapabilityVersions) != 1 {
			t.Fatalf("invalid variant: %+v", v)
		}
	}
	for _, w := range c.Workers {
		if w.Eligible || w.Reason == nil || w.Reason.Code != "missing_required_capability" {
			t.Fatalf("invalid evidence: %+v", w)
		}
	}
}
