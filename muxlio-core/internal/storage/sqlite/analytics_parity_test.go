package sqlite

import (
	"fmt"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
)

func seedAnalyticsFixture(mem *state.Memory, sql *Store, t *testing.T) {
	t.Helper()
	specs := []struct {
		id, wf, st string
		at         time.Time
		executors  []string
	}{
		{"run-1", "Deploy-Prod", "succeeded", time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC), []string{"Generic/A"}},
		{"run-2", "deploy-prod-nightly", "failed", time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC), []string{"generic/b"}},
		{"run-3", "other", "succeeded", time.Date(2026, 6, 2, 23, 59, 59, 0, time.UTC), []string{"Generic/A", "extra/c"}},
	}
	for _, spec := range specs {
		run := &state.Run{
			ID: spec.id, WorkflowName: spec.wf, State: spec.st,
			CreatedAt: spec.at, UpdatedAt: spec.at,
		}
		if err := mem.CreateRun(run); err != nil {
			t.Fatal(err)
		}
		if err := sql.CreateRun(run); err != nil {
			t.Fatal(err)
		}
		for i, ex := range spec.executors {
			a := state.ExecutionAttempt{
				ID: fmt.Sprintf("%s-a%d", spec.id, i), RunID: spec.id,
				Executor: ex, State: "succeeded",
				StartedAt: spec.at, FinishedAt: spec.at,
			}
			if err := mem.RecordExecutionAttempt(a); err != nil {
				t.Fatal(err)
			}
			if err := sql.RecordExecutionAttempt(a); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func snapshotSig(s *state.AnalyticsSnapshot) (total int, states map[string]int, runIDs []string) {
	states = map[string]int{}
	for k, v := range s.StateCounts {
		states[k] = v
	}
	runIDs = make([]string, 0, len(s.Runs))
	for _, r := range s.Runs {
		runIDs = append(runIDs, r.ID)
	}
	return s.TotalRuns, states, runIDs
}

func assertAnalyticsParity(t *testing.T, mem *state.Memory, sql *Store, q state.AnalyticsQuery) {
	t.Helper()
	ms, err := mem.QueryAnalytics(q)
	if err != nil {
		t.Fatal(err)
	}
	ss, err := sql.QueryAnalytics(q)
	if err != nil {
		t.Fatal(err)
	}
	mt, mstates, mruns := snapshotSig(ms)
	st, sstates, sruns := snapshotSig(ss)
	if mt != st {
		t.Fatalf("total memory=%d sqlite=%d q=%+v", mt, st, q)
	}
	for k, v := range mstates {
		if sstates[k] != v {
			t.Fatalf("stateCounts[%q] memory=%d sqlite=%d q=%+v", k, v, sstates[k], q)
		}
	}
	if len(mstates) != len(sstates) {
		t.Fatalf("stateCounts keys memory=%v sqlite=%v q=%+v", mstates, sstates, q)
	}
	mset := map[string]bool{}
	for _, id := range mruns {
		mset[id] = true
	}
	for _, id := range sruns {
		if !mset[id] {
			t.Fatalf("sqlite page run %q not in memory page %v q=%+v", id, mruns, q)
		}
	}
	if len(mruns) != len(sruns) {
		t.Fatalf("page size memory=%d sqlite=%d m=%v s=%v q=%+v", len(mruns), len(sruns), mruns, sruns, q)
	}
	if ms.HasMore != ss.HasMore {
		t.Fatalf("hasMore memory=%v sqlite=%v q=%+v", ms.HasMore, ss.HasMore, q)
	}
	if ms.Offset != ss.Offset {
		t.Fatalf("offset memory=%d sqlite=%d q=%+v", ms.Offset, ss.Offset, q)
	}
	if ms.Limit != ss.Limit {
		t.Fatalf("limit memory=%d sqlite=%d q=%+v", ms.Limit, ss.Limit, q)
	}
	for _, id := range mruns {
		ma := ms.AttemptsByRun[id]
		sa := ss.AttemptsByRun[id]
		if len(ma) != len(sa) {
			t.Fatalf("attempts for %q memory=%d sqlite=%d q=%+v", id, len(ma), len(sa), q)
		}
	}
}

func TestQueryAnalyticsParityWithMemory(t *testing.T) {
	mem := state.New()
	defer mem.Close()
	sql := openTemp(t)
	seedAnalyticsFixture(mem, sql, t)

	for _, q := range []state.AnalyticsQuery{
		{Limit: 50},
		{Workflow: "deploy-prod", Limit: 50},
		{Workflow: "DEPLOY", Limit: 50},
		{Executor: "generic/a", Limit: 50},
		{State: "succeeded,failed", Limit: 50},
		{
			CreatedAfter:  time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC),
			CreatedBefore: time.Date(2026, 6, 2, 23, 59, 59, 0, time.UTC),
			Limit:         50,
		},
		{Offset: 1, Limit: 1},
	} {
		assertAnalyticsParity(t, mem, sql, q)
	}
}

func TestQueryAnalyticsBoundaryMatchesMemory(t *testing.T) {
	mem := state.New()
	defer mem.Close()
	sql := openTemp(t)
	edge := time.Date(2026, 6, 2, 12, 0, 0, 123456789, time.UTC)
	run := &state.Run{ID: "edge", WorkflowName: "wf", State: "succeeded", CreatedAt: edge, UpdatedAt: edge}
	if err := mem.CreateRun(run); err != nil {
		t.Fatal(err)
	}
	if err := sql.CreateRun(run); err != nil {
		t.Fatal(err)
	}
	q := state.AnalyticsQuery{
		CreatedAfter: edge, CreatedBefore: edge, Limit: 10,
	}
	assertAnalyticsParity(t, mem, sql, q)
}

func TestQueryAnalyticsReturnsAllAttemptsForRun(t *testing.T) {
	sql := openTemp(t)
	at := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	if err := sql.CreateRun(&state.Run{ID: "heavy", WorkflowName: "wf", State: "succeeded", CreatedAt: at, UpdatedAt: at}); err != nil {
		t.Fatal(err)
	}
	const n = analyticsAttemptPageSize*2 + 5
	for i := 0; i < n; i++ {
		if err := sql.RecordExecutionAttempt(state.ExecutionAttempt{
			ID: fmt.Sprintf("a-%04d", i), RunID: "heavy", ActivationID: fmt.Sprintf("act-%04d", i),
			Executor: "generic/x", State: "succeeded", StartedAt: at, FinishedAt: at,
		}); err != nil {
			t.Fatal(err)
		}
	}
	snap, err := sql.QueryAnalytics(state.AnalyticsQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.AttemptsByRun["heavy"]) != n {
		t.Fatalf("got %d attempts want %d", len(snap.AttemptsByRun["heavy"]), n)
	}
	for i, attempt := range snap.AttemptsByRun["heavy"] {
		want := fmt.Sprintf("a-%04d", i)
		if attempt.ID != want {
			t.Fatalf("attempt[%d]=%q want %q", i, attempt.ID, want)
		}
	}
}
