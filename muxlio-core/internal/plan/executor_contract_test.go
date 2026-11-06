package plan

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestExecutorContractRequirementsReachDispatch(t *testing.T) {
	var node Node
	if err := json.Unmarshal([]byte(`{"id":"task","kind":"execute","executor":"test","executorVersion":"2","contractVersion":"1","requiredCapabilities":{"progress":"1"},"sessionId":"session","remoteTask":{"id":"task","url":"https://example.test/task"}}`), &node); err != nil {
		t.Fatal(err)
	}
	store := state.New()
	if err := store.CreateRun(&state.Run{ID: "run"}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateActivation(&state.Activation{ID: "run:task", RunID: "run", NodeID: "task", State: "running"}); err != nil {
		t.Fatal(err)
	}
	executor := workers.NewFake()
	engine := NewEngineWithStore(&Plan{}, executor, store)
	if _, err := engine.executeExecute("run", &node, nil, &EvalContext{}); err != nil {
		t.Fatal(err)
	}
	calls := executor.Calls()
	if len(calls) != 1 {
		t.Fatalf("dispatches: %d", len(calls))
	}
	req := calls[0]
	if req.ExecutorVersion != node.ExecutorVersion || req.ContractVersion != node.ContractVersion || req.SessionID != node.SessionID || !reflect.DeepEqual(req.RequiredCapabilities, node.RequiredCapabilities) || !reflect.DeepEqual(req.RemoteTask, node.RemoteTask) {
		t.Fatalf("contract requirements lost: %+v", req)
	}
}
