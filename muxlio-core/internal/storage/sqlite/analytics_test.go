package sqlite

import (
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
)

func TestAnalyticsWorkflowFilterDoesNotMatchRunID(t *testing.T) {
	s := openTemp(t)
	if err := s.CreateRun(&state.Run{ID: "deploy-prod", WorkflowName: "nightly"}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.QueryAnalytics(state.AnalyticsQuery{Workflow: "deploy-prod", CreatedAfter: time.Time{}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.TotalRuns != 0 || len(snapshot.Runs) != 0 {
		t.Fatalf("workflow filter matched run ID: %+v", snapshot)
	}
}

func TestAnalyticsExecutorFilterSelectsRunsWithMatchingAttempts(t *testing.T) {
	s := openTemp(t)
	for _, item := range []struct{ id, workflow string }{{"run-a", "one"}, {"run-b", "two"}} {
		if err := s.CreateRun(&state.Run{ID: item.id, WorkflowName: item.workflow}); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []struct{ id, run, executor string }{{"a", "run-a", "generic/a"}, {"b", "run-b", "generic/b"}} {
		if err := s.RecordExecutionAttempt(state.ExecutionAttempt{ID: item.id, RunID: item.run, Executor: item.executor, State: "succeeded"}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := s.QueryAnalytics(state.AnalyticsQuery{Executor: "generic/a", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.TotalRuns != 1 || len(snapshot.Runs) != 1 || snapshot.Runs[0].ID != "run-a" {
		t.Fatalf("executor filter returned unexpected runs: %+v", snapshot)
	}
}

func TestQueryAnalyticsClampsOffsetToTotal(t *testing.T) {
	s := openTemp(t)
	if err := s.CreateRun(&state.Run{ID: "one", WorkflowName: "demo", State: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.QueryAnalytics(state.AnalyticsQuery{Offset: 99, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Offset != snapshot.TotalRuns || snapshot.TotalRuns != 1 || len(snapshot.Runs) != 0 || snapshot.HasMore {
		t.Fatalf("unexpected pagination metadata: offset=%d total=%d runs=%d hasMore=%v", snapshot.Offset, snapshot.TotalRuns, len(snapshot.Runs), snapshot.HasMore)
	}
}
