package sqlite

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
)

func TestProgressIngestionBudgetsAndGenerationDeduplication(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			var s state.Store = state.New()
			if backend == "sqlite" {
				db, err := Open(filepath.Join(t.TempDir(), "progress.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				s = db
			}
			if err := s.CreateRun(&state.Run{ID: "run"}); err != nil {
				t.Fatal(err)
			}
			appendChunk := func(c state.LogChunk) {
				t.Helper()
				c.RunID, c.ActivationID = "run", "act"
				if err := s.AppendLogChunk(c); err != nil {
					t.Fatal(err)
				}
			}
			for seq := 1; seq <= 20; seq++ {
				// Multibyte text must be bounded by bytes, not characters.
				appendChunk(state.LogChunk{AttemptID: "text", Stream: "agent", Seq: seq, Data: []byte(strings.Repeat("é", 32<<10))})
			}
			for seq := 1; seq <= 18; seq++ {
				appendChunk(state.LogChunk{AttemptID: "bytes", Stream: "snapshot", Seq: seq, Snapshot: &state.ArtifactSnapshot{Name: "report", Digest: "digest", Size: 4 << 20}})
			}
			for seq := 1; seq <= state.MaxAttemptSnapshots+2; seq++ {
				appendChunk(state.LogChunk{AttemptID: "count", Stream: "snapshot", Seq: seq, Snapshot: &state.ArtifactSnapshot{Name: "empty", Digest: "digest"}})
			}
			for _, generation := range []int64{1, 1, 2, 2} {
				appendChunk(state.LogChunk{AttemptID: "replay", AssignmentID: "assignment", Generation: generation, CoordinatorEpoch: 1, Stream: "agent", Seq: 1, Data: []byte("progress")})
			}
			counts := map[string]int{}
			events, err := s.GetEvents("run", 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range events {
				counts[event.SubjectID]++
			}
			for attempt, want := range map[string]int{"text": 16, "bytes": 16, "count": state.MaxAttemptSnapshots, "replay": 2} {
				if counts[attempt] != want {
					t.Errorf("%s events=%d want %d", attempt, counts[attempt], want)
				}
			}
		})
	}
}

func TestProgressReconnectRestartBoundsAndAttemptIsolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "progress.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRun(&state.Run{ID: "run"}); err != nil {
		t.Fatal(err)
	}
	for _, attempt := range []string{"first", "retry"} {
		for seq := 1; seq <= state.MaxAttemptProgressEvents+2; seq++ {
			chunk := state.LogChunk{RunID: "run", ActivationID: "act", AttemptID: attempt, Stream: "agent", Seq: seq, Data: []byte("progress"), Timestamp: time.Now()}
			if err := s.AppendLogChunk(chunk); err != nil {
				t.Fatal(err)
			}
			if err := s.AppendLogChunk(chunk); err != nil {
				t.Fatal(err)
			}
		}
	}
	page, err := s.GetEvents("run", 0, 4)
	if err != nil || len(page) != 4 {
		t.Fatalf("first page: %v %v", page, err)
	}
	cursor := page[len(page)-1].Seq
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	replayed, err := s.GetEvents("run", cursor, 0)
	if err != nil || len(replayed) != 1+2*state.MaxAttemptProgressEvents-len(page) {
		t.Fatalf("reconnect: count=%d err=%v", len(replayed), err)
	}
	for i, event := range replayed {
		if event.Seq != cursor+i+1 {
			t.Fatalf("sequence gap: %+v", event)
		}
	}
	logs, err := s.GetLogChunks("run", "act")
	if err != nil || len(logs) != 2*state.MaxAttemptProgressEvents {
		t.Fatalf("bounded logs: %d %v", len(logs), err)
	}
}

func TestSnapshotHistorySurvivesFailedAttemptAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshots.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRun(&state.Run{ID: "run"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateActivation(&state.Activation{ID: "act", RunID: "run", State: "running"}); err != nil {
		t.Fatal(err)
	}
	for seq, digest := range []string{"first", "second"} {
		if err := s.AppendLogChunk(state.LogChunk{RunID: "run", ActivationID: "act", AttemptID: "attempt", Stream: "snapshot", Seq: seq + 1, Snapshot: &state.ArtifactSnapshot{Name: "report", Digest: digest, Size: 5}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetActivationState("act", "failed"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	history, err := state.ListSnapshots(s, "run", "act", "attempt", false)
	if err != nil || len(history) != 2 {
		t.Fatalf("history: %+v %v", history, err)
	}
	current, err := state.ListSnapshots(s, "run", "act", "", true)
	if err != nil || len(current) != 1 || current[0].Seq != history[1].Seq {
		t.Fatalf("current: %+v %v", current, err)
	}
	act, err := s.GetActivation("act")
	if err != nil || len(act.Outputs) != 0 || act.State != "failed" {
		t.Fatalf("snapshot published final output: %+v %v", act, err)
	}
	logs, err := s.GetLogChunks("run", "act")
	if err != nil || len(logs) != 0 {
		t.Fatalf("snapshot leaked into logs: %+v %v", logs, err)
	}
}
