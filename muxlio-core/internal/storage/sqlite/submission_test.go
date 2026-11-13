package sqlite

import (
	"fmt"
	"github.com/muxlio/muxlio-core/internal/artifacts"
	"github.com/muxlio/muxlio-core/internal/state"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestSubmissionAcrossConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.db")
	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := a
			if i%2 != 0 {
				s = b
			}
			id, _, err := s.CreateRunIdempotent(&state.Run{ID: fmt.Sprint(i), PlanDigest: "digest"}, "key", "input")
			if err != nil || id == "" {
				t.Errorf("concurrent submit: %s %v", id, err)
			}
		}(i)
	}
	wg.Wait()
	runs, err := a.ListRuns()
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs=%d err=%v", len(runs), err)
	}
	events, err := a.GetEvents(runs[0].ID, 0, 100)
	if err != nil || len(events) != 1 {
		t.Fatalf("creation events=%d err=%v", len(events), err)
	}
}

func TestSubmissionKeyRetention(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	local, err := artifacts.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"expired", "pinned", "active", "deadline", "parent", "child"} {
		if _, _, err := s.CreateRunIdempotent(&state.Run{ID: id}, id, ""); err != nil {
			t.Fatal(err)
		}
		if id != "active" && id != "child" {
			if err := s.SetRunState(id, "succeeded"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.PinRetention("run", "pinned", true); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(48 * time.Hour)
	until := now.Add(time.Hour)
	if err := s.SetRunRetention("deadline", "", &until); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRunRetention("child", "parent", nil); err != nil {
		t.Fatal(err)
	}
	policy := state.DefaultRetention()
	report, err := s.CollectGarbage(local, policy, now, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.IdempotencyKeys) != 1 || report.IdempotencyKeys[0] != "expired" {
		t.Fatalf("candidates: %v", report.IdempotencyKeys)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM run_idempotency_keys`).Scan(&count); err != nil || count != 6 {
		t.Fatalf("preview mutated keys: %d %v", count, err)
	}
	if _, err := s.CollectGarbage(local, policy, now, report.Token); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"pinned", "active", "deadline", "parent", "child"} {
		got, dup, err := s.CreateRunIdempotent(&state.Run{ID: "new-" + id}, id, "")
		if err != nil || !dup || got != id {
			t.Fatalf("protected %s: %s %v %v", id, got, dup, err)
		}
	}
	got, dup, err := s.CreateRunIdempotent(&state.Run{ID: "new-expired"}, "expired", "")
	if err != nil || dup || got != "new-expired" {
		t.Fatalf("expiry: %s %v %v", got, dup, err)
	}
}
