package plan

import (
	"encoding/json"
	"testing"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

type snapshotResultExecutor struct {
	store state.Store
	fail  bool
}

func (x snapshotResultExecutor) Execute(req workers.ExecuteRequest) (workers.ExecuteResult, error) {
	err := x.store.AppendLogChunk(state.LogChunk{RunID: req.RunID, ActivationID: req.ActivationID, AttemptID: req.AttemptID, Stream: "snapshot", Seq: 1,
		Snapshot: &state.ArtifactSnapshot{Name: "report", Digest: "sha256:draft", Size: 5}})
	result := workers.ExecuteResult{Outputs: map[string]any{"report": map[string]any{"digest": "sha256:final", "path": "report.txt"}}}
	if x.fail {
		result.Error = "failed after producing a draft"
	}
	return result, err
}

func TestSnapshotsNeverCommitFailedAttemptOutputs(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			s := state.New()
			p := &Plan{IRVersion: CurrentIRVersion, Name: "snapshots", Root: "root", Subgraphs: map[string]Subgraph{
				"root": {Nodes: []Node{{ID: "task", Kind: "execute", Executor: "test", Outputs: map[string]OutputDecl{"report": {Path: "report.txt"}}}}},
			}}
			e := NewEngineWithStore(p, snapshotResultExecutor{store: s, fail: fail}, s)
			id, err := e.CreateRun(json.RawMessage(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			if err := e.Run(id); (err != nil) != fail {
				t.Fatalf("run: %v", err)
			}
			acts, err := s.ListActivations(id)
			if err != nil || len(acts) != 1 {
				t.Fatalf("activations: %+v %v", acts, err)
			}
			act := acts[0]
			if fail {
				if act.State != ActFailed || len(act.Outputs) != 0 {
					t.Fatalf("failed attempt committed output: %+v", act)
				}
			} else if act.State != ActSucceeded || act.Outputs["report"].(map[string]any)["digest"] != "sha256:final" {
				t.Fatalf("final output not committed: %+v", act)
			}
			history, err := state.ListSnapshots(s, id, act.ID, "", false)
			if err != nil || len(history) != 1 {
				t.Fatalf("lost intermediate snapshot: %+v %v", history, err)
			}
		})
	}
}
