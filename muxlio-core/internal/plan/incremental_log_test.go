package plan

import (
	"testing"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

type streamingExecutor struct {
	t     *testing.T
	store state.Store
}

func (x streamingExecutor) Execute(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
	if req.LogSink == nil {
		x.t.Fatal("expected LogSink during execution")
	}
	if err := req.LogSink.Append("agent", []byte("partial")); err != nil {
		return workers.ExecuteResult{}, err
	}
	logs, err := x.store.GetLogChunks(req.RunID, req.ActivationID)
	if err != nil || len(logs) != 1 || string(logs[0].Data) != "partial" {
		x.t.Fatalf("incremental chunk not persisted during execution: %+v err=%v", logs, err)
	}
	return workers.ExecuteResult{}, nil
}

func TestIncrementalAgentLogsPersistBeforeAttemptCompletes(t *testing.T) {
	s := state.New()
	if err := s.CreateRun(&state.Run{ID: "r"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateActivation(&state.Activation{ID: "r:task", RunID: "r", NodeID: "task", State: "running"}); err != nil {
		t.Fatal(err)
	}
	e := NewEngineWithStore(&Plan{}, streamingExecutor{t, s}, s)
	if _, err := e.executeExecute("r", &Node{ID: "task", Executor: "test"}, nil, &EvalContext{}); err != nil {
		t.Fatal(err)
	}
}
