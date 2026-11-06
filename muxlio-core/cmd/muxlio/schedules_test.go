package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/schedule"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestScheduledRunLaunchAndRecovery(t *testing.T) {
	s, err := sqlite.Open(filepath.Join(t.TempDir(), "coordinator.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := &plan.Plan{Name: "scheduled", IRVersion: "1.0", Root: "root", Subgraphs: map[string]plan.Subgraph{"root": {Nodes: []plan.Node{{ID: "task", Kind: plan.KindExecute, Executor: "test/task"}}}}}
	digest, err := p.ComputeDigest()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(p)
	if err := s.SaveWorkflowVersion(digest, raw); err != nil {
		t.Fatal(err)
	}
	d := schedule.Definition{ID: "once", WorkflowDigest: digest, Input: json.RawMessage(`false`), Kind: "once", StartAt: time.Now().Add(-time.Second), Overlap: "allow", MaxCatchUp: 1}
	if err := s.CreateSchedule(d); err != nil {
		t.Fatal(err)
	}
	ids, err := s.TickSchedules(time.Now())
	if err != nil || len(ids) != 1 {
		t.Fatalf("tick=%v %v", ids, err)
	}
	x := workers.NewFake()
	r := &serverRunner{store: s, exec: x, engines: map[string]*plan.Engine{}, workers: map[string]chan struct{}{}, ctx: context.Background()}
	// A committed run is picked up again on the next tick even if launch was interrupted.
	ready, err := s.TickSchedules(time.Now())
	if err != nil || len(ready) != 1 || ready[0] != ids[0] {
		t.Fatalf("lost unlaunched run=%v %v", ready, err)
	}
	if err := r.StartPersistedRun(ids[0]); err != nil {
		t.Fatal(err)
	}
	if err := r.StartPersistedRun(ids[0]); err != nil {
		t.Fatal(err)
	}
	r.wg.Wait()
	if err := r.StartPersistedRun(ids[0]); err != nil {
		t.Fatal(err)
	}
	run, err := s.GetRun(ids[0])
	if err != nil || run.State != plan.RunSucceeded {
		t.Fatalf("run=%+v %v", run, err)
	}
	if len(x.Calls()) != 1 {
		t.Fatalf("duplicate execution: %d", len(x.Calls()))
	}
}
