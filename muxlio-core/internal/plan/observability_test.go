package plan

import (
	"strings"
	"testing"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

type observingExecutor struct {
	t     *testing.T
	store state.Store
}

func (x observingExecutor) Execute(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
	attempts, err := x.store.ListExecutionAttempts(req.RunID, req.ActivationID)
	if err != nil || len(attempts) != 1 || attempts[0].State != "running" || !attempts[0].FinishedAt.IsZero() {
		x.t.Fatalf("attempt must be visible during execution: %+v, %v", attempts, err)
	}
	return workers.ExecuteResult{Logs: strings.Repeat("diagnostic\n", 10000)}, nil
}

func TestExecutionVisibilityAndBoundedLogs(t *testing.T) {
	s := state.New()
	if err := s.CreateRun(&state.Run{ID: "r"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateActivation(&state.Activation{ID: "r:task", RunID: "r", NodeID: "task", State: "running"}); err != nil {
		t.Fatal(err)
	}
	e := NewEngineWithStore(&Plan{}, observingExecutor{t, s}, s)
	if _, err := e.executeExecute("r", &Node{ID: "task", Executor: "test"}, nil, &EvalContext{}); err != nil {
		t.Fatal(err)
	}
	attempts, err := s.ListExecutionAttempts("r", "r:task")
	if err != nil || len(attempts) != 1 || attempts[0].State != "succeeded" || attempts[0].FinishedAt.IsZero() {
		t.Fatalf("attempts=%+v err=%v", attempts, err)
	}
	logs, err := s.GetLogChunks("r", "r:task")
	if err != nil || len(logs) != 1 || len(logs[0].Data) > 64*1024 || !strings.Contains(string(logs[0].Data), "[log truncated]") {
		t.Fatalf("bounded log not persisted: chunks=%d err=%v", len(logs), err)
	}
}
