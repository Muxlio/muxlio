package sqlite

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
)

func TestInputDeadlineKeepsSubMillisecondOrder(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "inputs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	base := time.Date(2026, 11, 16, 9, 0, 0, 100, time.UTC)
	later := base.Add(time.Nanosecond)
	var after int
	if err := s.db.QueryRow(`SELECT muxlio_time_after(?, ?)`, later.Format(time.RFC3339Nano), base.Format(time.RFC3339Nano)).Scan(&after); err != nil || after != 1 {
		t.Fatalf("after=%d %v", after, err)
	}
	if err := s.db.QueryRow(`SELECT muxlio_time_after(?, ?)`, base.Format(time.RFC3339Nano), base.Format(time.RFC3339Nano)).Scan(&after); err != nil || after != 0 {
		t.Fatalf("equal timestamps were ordered: %d %v", after, err)
	}
}

func TestSetRunOutputOnCancelledRun(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "output.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateRun(&state.Run{ID: "run", State: "cancelled"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRunOutput("run", map[string]any{"ok": true}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRunOutput("missing", map[string]any{"ok": true}); err == nil {
		t.Fatal("missing run was accepted")
	}
}
