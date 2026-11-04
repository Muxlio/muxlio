package workers

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCommandSSHAgentEnvironment(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "/test/ambient-agent.sock")
	for _, tc := range []struct {
		name, executor, socket string
		host, isolated, want   bool
	}{
		{"authorized host command", "command", "/test/worker-agent.sock", true, false, true},
		{"disabled host command", "command", "/test/worker-agent.sock", false, false, false},
		{"isolated command", "command", "/test/worker-agent.sock", true, true, false},
		{"agent adapter", "agent/codex", "/test/worker-agent.sock", true, false, false},
		{"unconfigured socket", "command", "", true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewScriptExecutor(t.TempDir(), t.TempDir())
			s.AllowCommandHost = tc.host
			s.SSHAgentSocket = tc.socket
			env, err := s.environment(t.TempDir(), tc.isolated, tc.executor)
			if err != nil {
				t.Fatal(err)
			}
			found := strings.Contains(strings.Join(env, "\n"), "SSH_AUTH_SOCK=/test/worker-agent.sock")
			if found != tc.want {
				t.Fatalf("SSH agent forwarded = %v, want %v", found, tc.want)
			}
			if strings.Contains(strings.Join(env, "\n"), "/test/ambient-agent.sock") {
				t.Fatal("worker inherited the ambient SSH agent")
			}
		})
	}
}

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

func TestSSHAgentSocketMustBeAbsolute(t *testing.T) {
	s := NewScriptExecutor(t.TempDir(), t.TempDir())
	s.AllowCommandHost = true
	s.SSHAgentSocket = "relative.sock"
	if _, err := s.environment(t.TempDir(), false, "command"); err == nil {
		t.Fatal("worker accepted a relative SSH agent socket path")
	}
}
