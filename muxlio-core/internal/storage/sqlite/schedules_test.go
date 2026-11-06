package sqlite

import (
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/schedule"
)

func scheduleFixture(t *testing.T, s *Store) schedule.Definition {
	t.Helper()
	p := &plan.Plan{Name: "clock", IRVersion: "1.0", Root: "root", InputSchema: json.RawMessage(`{"type":"integer"}`), Subgraphs: map[string]plan.Subgraph{"root": {Nodes: []plan.Node{{ID: "task", Kind: plan.KindExecute, Executor: "test/echo"}}}}}
	digest, err := p.ComputeDigest()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveWorkflowVersion(digest, raw); err != nil {
		t.Fatal(err)
	}
	return schedule.Definition{ID: "clock", WorkflowDigest: digest, Input: json.RawMessage(`42`), Kind: "interval", StartAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), IntervalSeconds: 60, Overlap: "allow", MaxCatchUp: 3}
}

func TestScheduleRestartPolicies(t *testing.T) {
	for _, mode := range []string{"allow", "skip", "queue"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.db")
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			d := scheduleFixture(t, s)
			d.Overlap = mode
			if err := s.CreateSchedule(d); err != nil {
				t.Fatal(err)
			}
			// Restart before any fire: all due occurrences remain eligible.
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			now := d.StartAt.Add(5 * time.Minute)
			ids, err := s.TickSchedules(now)
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if mode == "allow" {
				want = 3
			}
			if len(ids) != want {
				t.Fatalf("runs=%v", ids)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			// Restart after commit, before launching: recover the exact same pins.
			engines, err := plan.RecoverRuns(s, nil)
			if err != nil || len(engines) != want {
				t.Fatalf("recover count=%d err=%v", len(engines), err)
			}
			for _, id := range ids {
				if engines[id] == nil {
					t.Fatalf("lost committed run %s", id)
				}
				r, err := s.GetRun(id)
				if err != nil || r.PlanDigest != d.WorkflowDigest || string(r.PlanJSON) == "" || r.Input != float64(42) {
					t.Fatalf("pin/input=%+v err=%v", r, err)
				}
			}
			if _, err := s.TickSchedules(now); err != nil {
				t.Fatal(err)
			}
			fires, err := s.ListScheduleFires(d.ID)
			if err != nil || len(fires) != 4 {
				t.Fatalf("fires=%+v err=%v", fires, err)
			}
			for i := 0; i < 3; i++ {
				expected := "started"
				if i > 0 && mode == "skip" {
					expected = "overlap_skipped"
				}
				if i > 0 && mode == "queue" {
					expected = "queued"
				}
				if fires[i].State != expected {
					t.Fatalf("fire %d=%+v", i, fires[i])
				}
			}
			if fires[3].State != "catch_up_limit" || fires[3].Through == nil || !fires[3].Through.Equal(now) {
				t.Fatalf("unrecorded missed fires: %+v", fires[3])
			}
			for _, id := range ids {
				if err := s.SetRunState(id, "succeeded"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.TickSchedules(now); err != nil {
				t.Fatal(err)
			}
			runs, err := s.ListRuns()
			if err != nil {
				t.Fatal(err)
			}
			if mode == "queue" {
				want++
			}
			if len(runs) != want {
				t.Fatalf("duplicate/lost queue: runs=%d want=%d", len(runs), want)
			}
			if mode == "queue" {
				fires, _ = s.ListScheduleFires(d.ID)
				if fires[1].State != "started" || fires[2].State != "queued" {
					t.Fatalf("queue order=%+v", fires)
				}
				if err := s.SetRunState(fires[1].RunID, "cancelled"); err != nil {
					t.Fatal(err)
				}
				if _, err := s.TickSchedules(now); err != nil {
					t.Fatal(err)
				}
				fires, _ = s.ListScheduleFires(d.ID)
				if fires[2].State != "started" {
					t.Fatal("queue lost across restart")
				}
			}
			items, err := s.ListSchedules()
			if err != nil || len(items) != 1 || !items[0].NextFire.Equal(d.StartAt.Add(6*time.Minute)) {
				t.Fatalf("cursor=%+v err=%v", items, err)
			}
		})
	}
}

func TestScheduleTickRollbackAndOnce(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	d := scheduleFixture(t, s)
	d.Kind = "once"
	d.IntervalSeconds = 0
	if err := s.CreateSchedule(d); err != nil {
		t.Fatal(err)
	}
	if ids, err := s.TickSchedules(d.StartAt.Add(-time.Nanosecond)); err != nil || len(ids) != 0 {
		t.Fatalf("early fire: %v %v", ids, err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER fail_schedule_run BEFORE INSERT ON runs BEGIN SELECT RAISE(ABORT,'simulated crash'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TickSchedules(d.StartAt); err == nil {
		t.Fatal("expected injected failure")
	}
	fires, err := s.ListScheduleFires(d.ID)
	if err != nil || len(fires) != 0 {
		t.Fatalf("partially committed fires=%v err=%v", fires, err)
	}
	items, err := s.ListSchedules()
	if err != nil || items[0].NextFire == nil || !items[0].NextFire.Equal(d.StartAt) {
		t.Fatalf("lost cursor: %+v %v", items, err)
	}
	if _, err := s.db.Exec(`DROP TRIGGER fail_schedule_run`); err != nil {
		t.Fatal(err)
	}
	ids, err := s.TickSchedules(d.StartAt)
	if err != nil || len(ids) != 1 {
		t.Fatalf("retry=%v %v", ids, err)
	}
	if err := s.SetRunState(ids[0], "succeeded"); err != nil {
		t.Fatal(err)
	}
	if ids, err := s.TickSchedules(d.StartAt.Add(time.Hour)); err != nil || len(ids) != 0 {
		t.Fatalf("once repeated=%v %v", ids, err)
	}
	items, err = s.ListSchedules()
	if err != nil || items[0].NextFire != nil {
		t.Fatalf("once cursor=%v %v", items, err)
	}
}

func TestScheduleConcurrentTicks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	d := scheduleFixture(t, first)
	if err := first.CreateSchedule(d); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, s := range []*Store{first, second} {
		wg.Add(1)
		go func(s *Store) {
			defer wg.Done()
			if _, err := s.TickSchedules(d.StartAt.Add(2 * time.Minute)); err != nil {
				t.Error(err)
			}
		}(s)
	}
	wg.Wait()
	fires, err := first.ListScheduleFires(d.ID)
	if err != nil || len(fires) != 3 {
		t.Fatalf("fires=%v %v", fires, err)
	}
	runs, err := first.ListRuns()
	if err != nil || len(runs) != 3 {
		t.Fatalf("runs=%d %v", len(runs), err)
	}
}

func TestScheduleRequiresImmutableTarget(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	d := scheduleFixture(t, s)
	for _, change := range []func(*schedule.Definition){
		func(d *schedule.Definition) { d.WorkflowDigest = "latest" },
		func(d *schedule.Definition) { d.MaxCatchUp = 0 },
		func(d *schedule.Definition) { d.Overlap = "replace" },
		func(d *schedule.Definition) {
			d.Kind = "cron"
			d.IntervalSeconds = 0
			d.Cron = "* * * * *"
			d.Timezone = "Local"
		},
	} {
		bad := d
		change(&bad)
		if err := s.CreateSchedule(bad); err == nil {
			t.Fatalf("accepted invalid schedule %+v", bad)
		}
	}
	if err := s.CreateSchedule(d); err != nil {
		t.Fatal(err)
	}
	changed := d
	changed.Input = json.RawMessage(`43`)
	if err := s.CreateSchedule(changed); err == nil {
		t.Fatal("schedule target/input is mutable")
	}
	raw, err := s.GetWorkflowVersion(d.WorkflowDigest)
	if err != nil {
		t.Fatal(err)
	}
	var p plan.Plan
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	p.Name = "new version"
	digest, err := p.ComputeDigest()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(p)
	if err := s.SaveWorkflowVersion(digest, raw); err != nil {
		t.Fatal(err)
	}
	ids, err := s.TickSchedules(d.StartAt)
	if err != nil || len(ids) != 1 {
		t.Fatalf("tick=%v %v", ids, err)
	}
	run, err := s.GetRun(ids[0])
	if err != nil || run.WorkflowName != "clock" || run.PlanDigest != d.WorkflowDigest {
		t.Fatalf("repinned run=%+v %v", run, err)
	}
}

func TestScheduleCronRepeatAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	d := scheduleFixture(t, s)
	d.Kind = "cron"
	d.IntervalSeconds = 0
	d.Cron = "30 1 * * *"
	d.Timezone = "America/New_York"
	d.StartAt = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	if err := s.CreateSchedule(d); err != nil {
		t.Fatal(err)
	}
	first := time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC)
	ids, err := s.TickSchedules(first)
	if err != nil || len(ids) != 1 {
		t.Fatalf("first=%v %v", ids, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SetRunState(ids[0], "succeeded"); err != nil {
		t.Fatal(err)
	}
	secondIDs, err := s.TickSchedules(first.Add(time.Hour))
	if err != nil || len(secondIDs) != 1 || secondIDs[0] == ids[0] {
		t.Fatalf("second=%v %v", secondIDs, err)
	}
	fires, err := s.ListScheduleFires(d.ID)
	if err != nil || len(fires) != 2 || !fires[0].DueAt.Equal(first) || !fires[1].DueAt.Equal(first.Add(time.Hour)) {
		t.Fatalf("history=%v %v", fires, err)
	}
	if _, err := s.TickSchedules(first.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	fires, err = s.ListScheduleFires(d.ID)
	if err != nil || len(fires) != 2 {
		t.Fatalf("duplicated repeat=%v %v", fires, err)
	}
}
