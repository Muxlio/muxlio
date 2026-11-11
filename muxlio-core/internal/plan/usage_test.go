package plan

import (
	"encoding/json"
	"github.com/muxlio/muxlio-core/internal/testaccounting"
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
		Outputs:    map[string]any{"result": "ok"},
		Accounting: testaccounting.Tokens(1000, 500, 200),
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
	if attempts[0].Accounting == nil {
		t.Fatal("expected non-nil usage from executor result")
	}
	if attempts[0].ExecutionDurationMS == nil || *attempts[0].ExecutionDurationMS < 0 {
		t.Fatalf("worker execution duration should be measured: %+v", attempts[0])
	}
	if testaccounting.TokenValue(attempts[0].Accounting, "input") != 1000 || testaccounting.TokenValue(attempts[0].Accounting, "output") != 500 || testaccounting.TokenValue(attempts[0].Accounting, "cached") != 200 {
		t.Fatalf("usage mismatch: %+v", attempts[0].Accounting)
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
	if attempts[0].Accounting != nil {
		t.Fatalf("missing provider usage should be nil, not zero: %+v", attempts[0].Accounting)
	}
}

func TestExecuteResultUsageRoundTripsJSON(t *testing.T) {
	// Ensure that ExecuteResult with Usage serialises/deserialises so the
	// remote worker completion path (JSON marshal on worker, unmarshal on
	// coordinator) preserves the usage distinction.
	raw, err := json.Marshal(workers.ExecuteResult{
		Outputs:    map[string]any{"result": "ok"},
		Accounting: testaccounting.Tokens(10, 20, 5),
	})
	if err != nil {
		t.Fatal(err)
	}
	var got workers.ExecuteResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Accounting == nil || testaccounting.TokenValue(got.Accounting, "input") != 10 || testaccounting.TokenValue(got.Accounting, "output") != 20 || testaccounting.TokenValue(got.Accounting, "cached") != 5 {
		t.Fatalf("usage did not round-trip: %+v", got.Accounting)
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
	if got2.Accounting != nil {
		t.Fatalf("missing usage should be nil after JSON round-trip: %+v", got2.Accounting)
	}
	if strings.Contains(string(raw2), "usage") {
		t.Fatalf("missing usage should not appear in JSON: %s", raw2)
	}
}
