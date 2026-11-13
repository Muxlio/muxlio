package sqlite

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
)

func TestForkProvenanceSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := s.CreateRun(&state.Run{ID: "source", CreatedAt: createdAt}); err != nil {
		t.Fatal(err)
	}
	fork := &state.Run{ID: "fork", CreatedAt: createdAt.Add(time.Second), Fork: &state.ForkProvenance{SourceRunID: "source", SourcePlanDigest: "original", ReuseActivations: []string{"source:work"}}}
	if err := s.CreateRun(fork); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.GetRun("fork")
	if err != nil {
		t.Fatal(err)
	}
	if got.Fork == nil || got.Fork.SourceRunID != "source" || got.Fork.SourcePlanDigest != "original" || len(got.Fork.ReuseActivations) != 1 || got.Fork.ReuseActivations[0] != "source:work" {
		t.Fatalf("provenance: %+v", got.Fork)
	}
	var parent string
	if err := s.db.QueryRow(`SELECT parent_id FROM run_retention WHERE run_id='fork'`).Scan(&parent); err != nil || parent != "source" {
		t.Fatalf("retention ancestry: %s %v", parent, err)
	}
	runs, err := s.ListRuns()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("listed %d runs, want 2", len(runs))
	}
	// Explicit timestamps keep this persistence check independent of the clock.
	if source := runs[0]; source.ID != "source" || source.Fork != nil {
		t.Fatalf("listed source: %+v", source)
	}
	if forked := runs[1]; forked.ID != "fork" || !reflect.DeepEqual(forked.Fork, fork.Fork) {
		t.Fatalf("listed fork provenance: %+v, want %+v", forked, fork.Fork)
	}
}
