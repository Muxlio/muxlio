package contract

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestUnsupportedRemoteTaskReportsRequestedCapability(t *testing.T) {
	r := Request{Executor: "test", RequiredCapabilities: map[string]string{CapabilityRemoteTask: Version}}
	for i := 0; i < 50; i++ {
		var unsupported *UnsupportedCapabilityError
		if err := r.Check(&AdapterManifest{}); !errors.As(err, &unsupported) || unsupported.Capability != CapabilityRemoteTask {
			t.Fatalf("remoteTask requirement hidden by an implied capability: %v", err)
		}
	}
}

func TestPendingRemoteResultRequiresIdentity(t *testing.T) {
	for _, raw := range []string{
		`{"remoteTask":{"pending":true}}`,
		`{"error":"failed","remoteTask":{"id":"remote","pending":true}}`,
	} {
		if _, err := ParseResult([]byte(raw)); err == nil {
			t.Fatalf("accepted invalid handoff: %s", raw)
		}
	}
	result, err := ParseResult([]byte(`{"remoteTask":{"id":"remote","pending":true,"pollAt":"2026-11-06T17:00:00Z"}}`))
	if err != nil || !result.RemoteTask.Pending || result.RemoteTask.PollAt.IsZero() {
		t.Fatalf("handoff lost: %+v %v", result, err)
	}
}

func TestRemotePollingCapabilityAndIdentity(t *testing.T) {
	m := &AdapterManifest{Executor: "test", Capabilities: map[string]bool{CapabilityRemoteTask: true, CapabilityReconcile: true}}
	r := Request{Executor: "test", Action: "poll"}
	if err := r.Check(m); err == nil {
		t.Fatal("poll accepted without task identity")
	}
	r.RemoteTask = &RemoteTaskReference{ID: "remote"}
	if err := r.Check(m); err != nil {
		t.Fatal(err)
	}
	delete(m.Capabilities, CapabilityReconcile)
	if err := r.Check(m); err == nil {
		t.Fatal("async execution accepted without reconciliation")
	}
	m.Capabilities[CapabilityReconcile] = true
	r.SessionID = "local-session"
	if err := r.Check(m); err == nil {
		t.Fatal("async execution accepted a local session")
	}
	r.SessionID = ""
	r.Params = json.RawMessage(`{"sessionId":"local-session"}`)
	if err := r.Check(m); err == nil {
		t.Fatal("async execution accepted a legacy session in params")
	}
}

func TestRemoteContextSurvivesResultAndRequestRoundTrip(t *testing.T) {
	result, err := ParseResult([]byte(`{"remoteTask":{"id":"task","url":"https://example.test/rpc","contextId":"remote-context","pending":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(Request{Action: "poll", RemoteTask: result.RemoteTask})
	if err != nil {
		t.Fatal(err)
	}
	var request Request
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	if request.RemoteTask.ContextID != "remote-context" || request.RemoteTask.ID != "task" || request.RemoteTask.URL != "https://example.test/rpc" {
		t.Fatalf("remote reference lost: %+v", request.RemoteTask)
	}
}
