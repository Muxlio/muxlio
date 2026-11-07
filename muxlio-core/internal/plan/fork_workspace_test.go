package plan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestForkRecomputesDefaultWorkspaceReads(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(source, []byte("old revision"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	executor := remoteExecutor{execute: func(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
		calls++
		data, err := os.ReadFile(source)
		return workers.ExecuteResult{Outputs: map[string]any{"result": string(data)}}, err
	}}
	store := state.New()
	defer store.Close()
	p := forkTestPlan(t, "pure")
	p.Subgraphs["root"].Nodes[0].WorkspaceMode = ""
	original := NewEngineWithStore(p, executor, store)
	id, err := original.CreateRun(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := original.Run(id); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("new revision"), 0600); err != nil {
		t.Fatal(err)
	}
	fork := NewEngineWithStore(nil, executor, store)
	forkID, err := fork.ForkRun(id, nil, []string{id + ":work"})
	if err != nil {
		t.Fatal(err)
	}
	if err := fork.Run(forkID); err != nil {
		t.Fatal(err)
	}
	run, err := store.GetRun(forkID)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || run.Output != "new revision" {
		t.Fatalf("changed default workspace was not read: calls=%d, fork returned=%v", calls, run.Output)
	}
}
