package plan

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestRegisteredRunPreservesBytesAndWorkspaceOnRecovery(t *testing.T) {
	p := &Plan{Name: "registered", IRVersion: "1.0", Root: "root", Subgraphs: map[string]Subgraph{"root": {Nodes: []Node{{ID: "task", Kind: KindExecute, Executor: "generic/task"}}}}}
	if _, err := p.ComputeDigest(); err != nil {
		t.Fatal(err)
	}
	// Even a non-default encoding is pinned byte for byte.
	raw, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	st := state.New()
	exec := workers.NewFake()
	engine := NewEngineWithStore(p, exec, st)
	id, dup, err := engine.CreateRegisteredRun(raw, json.RawMessage(`{}`), "authorized-project", "key", "input-digest")
	if err != nil || dup {
		t.Fatalf("create: %s %v %v", id, dup, err)
	}
	replay, dup, err := engine.CreateRegisteredRun(raw, json.RawMessage(`{}`), "authorized-project", "key", "input-digest")
	if err != nil || !dup || replay != id {
		t.Fatalf("replay: %s %v %v", replay, dup, err)
	}
	// Simulate restart with an unrelated current plan.
	recovered := NewEngineWithStore(&Plan{}, exec, st)
	if err := recovered.Run(id); err != nil {
		t.Fatal(err)
	}
	run, err := st.GetRun(id)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(run.PlanJSON, raw) || run.PlanDigest != p.Digest || run.WorkspaceID != "authorized-project" {
		t.Fatalf("run pin changed: %+v", run)
	}
	calls := exec.Calls()
	if len(calls) != 1 || calls[0].WorkspaceID != "authorized-project" {
		t.Fatalf("dispatch: %+v", calls)
	}
}
