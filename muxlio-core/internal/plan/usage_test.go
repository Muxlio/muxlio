package plan

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestExecutorUsagePropagatesToAttempt(t *testing.T) {
	s := state.New()
	if err := s.CreateRun(&state.Run{ID: "r"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateActivation(&state.Activation{ID: "r:task", RunID: "r", NodeID: "task", State: "running"}); err != nil {
		t.Fatal(err)
	}
	exec := workers.NewFake().ByNode("task", workers.ExecuteResult{
		Outputs: map[string]any{"result": "ok"},
		Usage:   &state.TokenUsage{Input: 1000, Output: 500, Cached: 200},
	})
	e := NewEngineWithStore(&Plan{}, exec, s)
	if _, err := e.executeExecute("r", &Node{ID: "task", Executor: "test/echo", Outputs: map[string]OutputDecl{"result": {Mime: "text/plain"}}}, nil, &EvalContext{}); err != nil {
		t.Fatal(err)
	}
	attempts, err := s.ListExecutionAttempts("r", "r:task")
	if err != nil || len(attempts) != 1 {
		t.Fatalf("expected 1 attempt, got=%+v err=%v", attempts, err)
	}
	if attempts[0].State != "succeeded" {
		t.Fatalf("expected succeeded, got %q", attempts[0].State)
	}
	if attempts[0].Usage == nil {
		t.Fatal("expected non-nil usage from executor result")
	}
	if attempts[0].ExecutionDurationMS == nil || *attempts[0].ExecutionDurationMS < 0 {
		t.Fatalf("worker execution duration should be measured: %+v", attempts[0])
	}
	if attempts[0].Usage.Input != 1000 || attempts[0].Usage.Output != 500 || attempts[0].Usage.Cached != 200 {
		t.Fatalf("usage mismatch: %+v", attempts[0].Usage)
	}
}

func TestExecutorMissingUsageStaysNil(t *testing.T) {
	s := state.New()
	if err := s.CreateRun(&state.Run{ID: "r"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateActivation(&state.Activation{ID: "r:task", RunID: "r", NodeID: "task", State: "running"}); err != nil {
		t.Fatal(err)
	}
	// FakeExecutor with no Usage field set — provider did not report.
	exec := workers.NewFake().ByNode("task", workers.ExecuteResult{
		Outputs: map[string]any{"result": "ok"},
	})
	e := NewEngineWithStore(&Plan{}, exec, s)
	if _, err := e.executeExecute("r", &Node{ID: "task", Executor: "test/echo", Outputs: map[string]OutputDecl{"result": {Mime: "text/plain"}}}, nil, &EvalContext{}); err != nil {
		t.Fatal(err)
	}
	attempts, err := s.ListExecutionAttempts("r", "r:task")
	if err != nil || len(attempts) != 1 {
		t.Fatalf("expected 1 attempt, got=%+v err=%v", attempts, err)
	}
	if attempts[0].Usage != nil {
		t.Fatalf("missing provider usage should be nil, not zero: %+v", attempts[0].Usage)
	}
}

func TestExecuteResultUsageRoundTripsJSON(t *testing.T) {
	// Ensure that ExecuteResult with Usage serialises/deserialises so the
	// remote worker completion path (JSON marshal on worker, unmarshal on
	// coordinator) preserves the usage distinction.
	raw, err := json.Marshal(workers.ExecuteResult{
		Outputs: map[string]any{"result": "ok"},
		Usage:   &state.TokenUsage{Input: 10, Output: 20, Cached: 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got workers.ExecuteResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Usage == nil || got.Usage.Input != 10 || got.Usage.Output != 20 || got.Usage.Cached != 5 {
		t.Fatalf("usage did not round-trip: %+v", got.Usage)
	}

	// Missing usage omits the field entirely.
	raw2, err := json.Marshal(workers.ExecuteResult{Outputs: map[string]any{"result": "ok"}})
	if err != nil {
		t.Fatal(err)
	}
	var got2 workers.ExecuteResult
	if err := json.Unmarshal(raw2, &got2); err != nil {
		t.Fatal(err)
	}
	if got2.Usage != nil {
		t.Fatalf("missing usage should be nil after JSON round-trip: %+v", got2.Usage)
	}
	if strings.Contains(string(raw2), "usage") {
		t.Fatalf("missing usage should not appear in JSON: %s", raw2)
	}
}
