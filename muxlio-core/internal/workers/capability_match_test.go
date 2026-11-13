package workers

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
)

func TestExplainDispatchPreservesEnvironmentEvidence(t *testing.T) {
	descriptor := environmentDescriptor()
	req := ExecuteRequest{Executor: "test", Environment: descriptor.Environment}
	raw, _ := json.Marshal(req)
	caps := []state.WorkerCapability{{Name: "test", Contract: descriptor, Readiness: &contract.ProbeResult{Ready: true, Checks: []contract.ProbeCheck{{Name: contract.ProbeVersion, Passed: true, Runner: "runner", Version: "9.0.0"}}}}}
	reason := ExplainWorkerDispatch("worker", "test", raw, caps, nil)
	if reason == nil || reason.Code != "environment_runner_mismatch" || MatchWorkerDispatch("worker", "test", raw, caps, nil) {
		t.Fatalf("reason: %+v", reason)
	}
	caps[0].Readiness.Checks[0].Version = "1.2.3"
	if reason := ExplainWorkerDispatch("worker", "test", raw, caps, nil); reason != nil {
		t.Fatalf("compatible environment: %+v", reason)
	}
}

func TestExplainWorkerDispatchSessionOwnership(t *testing.T) {
	caps := []state.WorkerCapability{{Name: "test", Contract: &contract.AdapterManifest{Capabilities: map[string]bool{contract.CapabilitySession: true}, CapabilityVersions: map[string]string{contract.CapabilitySession: contract.SessionVersion}}}}
	req := ExecuteRequest{Executor: "test", Session: &contract.SessionRequest{Mode: "resume", Handle: &contract.SessionHandle{ID: "session", Executor: "test", RunnerVersion: "1.0.0", OwnerWorker: "owner", OwnerEnvironment: "env", ExpiresAt: time.Now().Add(time.Hour)}}}
	raw, _ := json.Marshal(req)
	labels := map[string]string{SessionEnvironmentLabel: "env"}
	if reason := ExplainWorkerDispatch("other", "test", raw, caps, labels); reason == nil || reason.Code != "session_owner_mismatch" {
		t.Fatalf("reason: %+v", reason)
	}
	if reason := ExplainWorkerDispatch("owner", "test", raw, caps, labels); reason != nil {
		t.Fatalf("owner rejected: %+v", reason)
	}
}

func TestExplainDispatchReportsMissingCapability(t *testing.T) {
	reason := ExplainDispatch("test", []byte(`{"requiredCapabilities":{"usage":"1"}}`), []state.WorkerCapability{{Name: "test"}}, nil)
	if reason == nil || reason.Code != "missing_required_capability" || reason.Capability != "usage" {
		t.Fatalf("reason: %+v", reason)
	}
}
