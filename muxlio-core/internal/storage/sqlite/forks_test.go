package sqlite

import (
	"path/filepath"
	"testing"

	"github.com/muxlio/muxlio-core/internal/state"
)

func TestForkProvenanceSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRun(&state.Run{ID: "source"}); err != nil {
		t.Fatal(err)
	}
	fork := &state.Run{ID: "fork", Fork: &state.ForkProvenance{SourceRunID: "source", SourcePlanDigest: "original", ReuseActivations: []string{"source:work"}}}
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
	if len(runs) != 2 || runs[1].Fork == nil {
		t.Fatalf("list provenance: %+v", runs)
	}
}
