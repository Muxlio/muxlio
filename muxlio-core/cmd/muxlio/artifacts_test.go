package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
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

func TestLocalRunWithoutHistoryDoesNotFallBackToServer(t *testing.T) {
	t.Setenv("MUXLIO_SERVER", "")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"defaultServer":"dead","servers":{"dead":"http://127.0.0.1:1"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(filepath.Join(root, "data", "coordinator.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRun(&state.Run{ID: "fresh", State: "created"}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	old, r, w := captureStdout(t)
	eventsCode := cmdEvents([]string{"fresh", "--data-dir", root, "--json"}, root, false)
	artifactsCode := cmdArtifacts([]string{"fresh", "--data-dir", root, "--json"}, root, false)
	w.Close()
	restoreStdout(old)
	_, _ = io.ReadAll(r)
	if eventsCode != 0 || artifactsCode != 0 {
		t.Fatalf("local run without history contacted server: events=%d artifacts=%d", eventsCode, artifactsCode)
	}
}
