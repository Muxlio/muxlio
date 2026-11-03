package workers

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCommandHostPolicyDefaultDenyAndSandboxFailClosed(t *testing.T) {
	s := NewScriptExecutor(t.TempDir(), t.TempDir())
	r, err := s.Execute(ExecuteRequest{Executor: "command", Params: json.RawMessage(`{"program":"/bin/echo"}`)})
	if err != nil || !strings.Contains(r.Error, "denied by worker policy") {
		t.Fatalf("default host policy: result=%+v err=%v", r, err)
	}
	s.AllowCommandHost = true
	r, err = s.Execute(ExecuteRequest{Executor: "command", Params: json.RawMessage(`{"program":"/bin/echo","execution":{"isolation":"sandbox"}}`)})
	if err != nil || !strings.Contains(r.Error, "no verified OCI backend") {
		t.Fatalf("sandbox must fail closed: result=%+v err=%v", r, err)
	}
}
