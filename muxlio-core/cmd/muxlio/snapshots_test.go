package main

import (
	"encoding/json"
	"io"
	"testing"

	"github.com/muxlio/muxlio-core/internal/state"
)

func TestLocalSnapshotsPaginationAndHistory(t *testing.T) {
	t.Setenv("MUXLIO_SERVER", "")
	dir := t.TempDir()
	s, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRun(&state.Run{ID: "run"}); err != nil {
		t.Fatal(err)
	}
	for seq := 1; seq <= 2; seq++ {
		if err := s.AppendLogChunk(state.LogChunk{RunID: "run", ActivationID: "act", AttemptID: "attempt", Stream: "snapshot", Seq: seq, Snapshot: &state.ArtifactSnapshot{Name: "report", Digest: "digest"}}); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	invoke := func(extra ...string) map[string]any {
		t.Helper()
		old, r, w := captureStdout(t)
		code := cmdSnapshots(append([]string{"run", "--data-dir", dir, "--json"}, extra...), "", false)
		w.Close()
		restoreStdout(old)
		raw, err := io.ReadAll(r)
		r.Close()
		if err != nil || code != 0 {
			t.Fatalf("snapshots: code=%d err=%v output=%s", code, err, raw)
		}
		var page map[string]any
		if err := json.Unmarshal(raw, &page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	first := invoke("--limit", "1")
	if first["hasMore"] != true || first["nextAfter"] != float64(2) {
		t.Fatalf("first: %v", first)
	}
	second := invoke("--limit", "1", "--since", "2")
	if second["hasMore"] != false || second["nextAfter"] != float64(3) {
		t.Fatalf("second: %v", second)
	}
	current := invoke("--current", "--attempt", "attempt")
	if len(current["snapshots"].([]any)) != 1 || current["nextAfter"] != float64(3) {
		t.Fatalf("current: %v", current)
	}
	if len(invoke("--attempt", "other")["snapshots"].([]any)) != 0 {
		t.Fatal("attempt filter ignored")
	}
}
