package state

import (
	"testing"
	"time"
)

func TestQueryAnalyticsRespectsCreatedAtAndState(t *testing.T) {
	s := New()
	defer s.Close()
	early := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	late := time.Date(2026, 1, 8, 12, 0, 0, 0, time.UTC)
	for _, spec := range []struct {
		id, st string
		at     time.Time
	}{
		{"old", "succeeded", early},
		{"new-ok", "succeeded", late},
		{"new-bad", "failed", late},
	} {
		if err := s.CreateRun(&Run{ID: spec.id, WorkflowName: "wf", CreatedAt: spec.at, UpdatedAt: spec.at}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetRunState(spec.id, spec.st); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordExecutionAttempt(ExecutionAttempt{
			ID: spec.id + "-a", RunID: spec.id, ActivationID: "act", Executor: "agent/a",
			State: "succeeded", StartedAt: spec.at, FinishedAt: spec.at,
			Usage: &TokenUsage{Input: 10, Output: 5},
		}); err != nil {
			t.Fatal(err)
		}
	}
	windowEnd := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	windowStart := time.Date(2026, 1, 7, 0, 0, 0, 0, time.UTC)
	snap, err := s.QueryAnalytics(AnalyticsQuery{
		CreatedAfter: windowStart, CreatedBefore: windowEnd, State: "succeeded", Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if snap.TotalRuns != 1 || len(snap.Runs) != 1 || snap.Runs[0].ID != "new-ok" {
		t.Fatalf("run filter: total=%d runs=%+v", snap.TotalRuns, snap.Runs)
	}
	if len(snap.AttemptsByRun["new-ok"]) != 1 {
		t.Fatalf("attempts=%v", snap.AttemptsByRun)
	}
	if snap.AttemptsByRun["new-ok"][0].Result != nil {
		t.Fatal("result payload should be stripped")
	}
	snap, err = s.QueryAnalytics(AnalyticsQuery{
		CreatedAfter: windowStart, CreatedBefore: windowEnd, Executor: "agent/b", Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if snap.TotalRuns != 0 || len(snap.Runs) != 0 {
		t.Fatalf("executor filter should exclude runs without matching attempts: %+v", snap)
	}
}
