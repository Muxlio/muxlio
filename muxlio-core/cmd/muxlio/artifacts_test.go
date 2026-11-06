package main

import (
	"testing"

	"github.com/muxlio/muxlio-core/internal/state"
)

func TestCollectContractArtifacts(t *testing.T) {
	acts := []state.Activation{{ID: "a", NodeID: "task", Outputs: map[string]any{
		"summary": map[string]any{"digest": "sha256:summary", "mime": "text/markdown", "path": "summary.md"},
		"nested":  []any{map[string]any{"file": map[string]any{"digest": "sha256:file"}}},
		"legacy":  map[string]any{"ref": "cas://legacy", "digest": "sha256:legacy"},
		"scalar":  "plain result",
	}}}
	got := collectArtifactsFromActivations(acts)
	if len(got) != 3 {
		t.Fatalf("artifacts=%+v", got)
	}
	for i, want := range []struct{ name, ref string }{{"legacy", "cas://legacy"}, {"nested[0].file", "sha256:file"}, {"summary", "sha256:summary"}} {
		if got[i].Name != want.name || got[i].Ref != want.ref || got[i].ActivationID != "a" || got[i].NodeID != "task" {
			t.Fatalf("artifact=%+v want=%+v", got[i], want)
		}
	}
}
