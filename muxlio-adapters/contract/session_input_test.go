package contract

import (
	"testing"
	"time"
)

func TestSessionInputRequiresDeclaredDeliveryGuarantees(t *testing.T) {
	handle := &SessionHandle{ID: "s", Executor: "test", RunnerVersion: "1", OwnerWorker: "w", OwnerEnvironment: "env", ExpiresAt: time.Now().Add(time.Hour)}
	manifest := &AdapterManifest{Executor: "test", Capabilities: map[string]bool{CapabilitySession: true}, CapabilityVersions: map[string]string{CapabilitySession: SessionVersion}}
	req := Request{Executor: "test", Session: &SessionRequest{Mode: "resume", Handle: handle}, SessionInputs: []SessionInput{{Version: "1", RequestID: "request", Message: "steer"}}}
	if err := req.Check(manifest); err == nil {
		t.Fatal("input silently emulated")
	}
	manifest.Capabilities[CapabilitySessionInput] = true
	if err := req.Check(manifest); err != nil {
		t.Fatal(err)
	}
	req.Action = "steer"
	if err := req.Check(manifest); err == nil {
		t.Fatal("live delivery silently emulated")
	}
	manifest.Capabilities[CapabilityLiveInput] = true
	if err := req.Check(manifest); err != nil {
		t.Fatal(err)
	}
	manifest.CapabilityVersions[CapabilityLiveInput] = "future"
	if err := req.Check(manifest); err == nil {
		t.Fatal("unknown live capability version accepted")
	}
	delete(manifest.CapabilityVersions, CapabilityLiveInput)
	req.SessionInputs = append(req.SessionInputs, req.SessionInputs[0])
	if err := req.Check(manifest); err == nil {
		t.Fatal("duplicate input identity accepted in one delivery")
	}
	req.SessionInputs = req.SessionInputs[:1]
	req.SessionInputs[0].Version = "future"
	if err := req.Check(manifest); err == nil {
		t.Fatal("unknown input version accepted")
	}
}
