package contract

import (
	"testing"
	"time"
)

func TestPersistentSessionContractFailsClosed(t *testing.T) {
	manifest := &AdapterManifest{Executor: "test", Capabilities: map[string]bool{CapabilitySession: true}, CapabilityVersions: map[string]string{CapabilitySession: SessionVersion}}
	valid := SessionHandle{ID: "opaque", Executor: "test", RunnerVersion: "runner-1", OwnerWorker: "worker", OwnerEnvironment: "environment", ExpiresAt: time.Now().Add(time.Hour), Recoverable: true}
	for _, mode := range []string{"create", "resume"} {
		req := Request{Executor: "test", Session: &SessionRequest{Mode: mode}}
		if mode == "resume" {
			req.Session.Handle = &valid
		}
		if err := req.Check(manifest); err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
	}
	for _, scenario := range []string{"absent", "expired", "ownership", "runner", "executor", "mode", "create-handle", "legacy", "revision"} {
		t.Run(scenario, func(t *testing.T) {
			handle := valid
			req := Request{Executor: "test", Session: &SessionRequest{Mode: "resume", Handle: &handle}}
			switch scenario {
			case "absent":
				req.Session.Handle = nil
			case "expired":
				handle.ExpiresAt = time.Now().Add(-time.Second)
			case "ownership":
				handle.OwnerWorker = ""
			case "runner":
				handle.RunnerVersion = ""
			case "executor":
				handle.Executor = "other"
			case "mode":
				req.Session.Mode = "unknown"
			case "create-handle":
				req.Session.Mode = "create"
			case "legacy":
				req.SessionID = "bare-id"
			case "revision":
				req.RequiredCapabilities = map[string]string{CapabilitySession: "1"}
			}
			if err := req.Check(manifest); err == nil {
				t.Fatal("unsafe session accepted")
			}
		})
	}
	legacy := *manifest
	legacy.CapabilityVersions = nil
	if err := (&Request{Executor: "test", Session: &SessionRequest{Mode: "create"}}).Check(&legacy); err == nil {
		t.Fatal("legacy session adapter accepted structured protocol")
	}
	if err := (&Request{Executor: "test"}).Check(nil); err != nil {
		t.Fatalf("one-shot default: %v", err)
	}
}
