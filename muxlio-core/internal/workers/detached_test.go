package workers

import (
	"context"
	"testing"
	"time"
)

func TestPendingResultStillWaitsForLocalProcessExit(t *testing.T) {
	s := testScript(t, `printf '{"remoteTask":{"id":"remote","pending":true}}'; sleep 0.2`)
	started := time.Now()
	result, err := s.Execute(ExecuteRequest{Context: context.Background(), Executor: "test", Outputs: map[string]OutputDecl{"future": {Path: "future.json"}}})
	if err != nil || result.Error != "" || result.RemoteTask == nil || !result.RemoteTask.Pending {
		t.Fatalf("pending result lost: %+v %v", result, err)
	}
	if time.Since(started) < 200*time.Millisecond {
		t.Fatal("local process detached before termination")
	}
	if len(result.Outputs) != 0 {
		t.Fatalf("pending result tried to collect future outputs: %+v", result.Outputs)
	}
}
