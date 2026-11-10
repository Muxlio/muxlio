package sqlite

import (
	"strings"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
)

var _ state.AnalyticsStore = (*Store)(nil)

func (s *Store) QueryAnalytics(q state.AnalyticsQuery) (*state.AnalyticsSnapshot, error) {
	// Read through the DB directly and let listExecutionAttemptPage acquire the
	// store lock for attempt pages. Holding it here would recursively lock.
	states := stateSplitCSV(q.State)
	// Keep workflow filtering consistent with the in-memory store: IDs are not
	// workflow names and must not create unexpected matches.
	where := ` WHERE instr(lower(workflow_name),lower(?))>0`
	args := []any{q.Workflow}
	if q.Executor != "" {
		where += ` AND EXISTS (SELECT 1 FROM execution_attempts ea WHERE ea.run_id=runs.id AND instr(lower(ea.executor),lower(?))>0)`
		args = append(args, q.Executor)
	}
	if len(states) == 1 {
		where += ` AND state=?`
		args = append(args, states[0])
	} else if len(states) > 1 {
		placeholders := strings.Repeat("?,", len(states)-1) + "?"
		where += ` AND state IN (` + placeholders + `)`
		for _, st := range states {
			args = append(args, st)
		}
	} else if q.State != "" {
		where += ` AND state=?`
		args = append(args, q.State)
	}
	if !q.CreatedAfter.IsZero() {
		where += ` AND created_at>=?`
		args = append(args, q.CreatedAfter.UTC().Format(time.RFC3339Nano))
	}
	if !q.CreatedBefore.IsZero() {
		where += ` AND created_at<=?`
		args = append(args, q.CreatedBefore.UTC().Format(time.RFC3339Nano))
	}
	var total int
	if err := s.db.QueryRow(`SELECT count(*) FROM runs`+where, args...).Scan(&total); err != nil {
		return nil, err
	}
	counts := map[string]int{}
	countRows, err := s.db.Query(`SELECT state,count(*) FROM runs`+where+` GROUP BY state`, args...)
	if err != nil {
		return nil, err
	}
	for countRows.Next() {
		var key string
		var n int
		if err := countRows.Scan(&key, &n); err != nil {
			countRows.Close()
			return nil, err
		}
		counts[key] = n
	}
	if err := countRows.Close(); err != nil {
		return nil, err
	}
	offset := q.Offset
	limit := q.Limit
	if limit <= 0 {
		limit = 200
	}
	query := `SELECT id,workflow_name,state,NULL,NULL,'',created_at,updated_at,NULL,plan_digest,EXISTS(SELECT 1 FROM retention_pins WHERE kind='run' AND retention_pins.id=runs.id),(SELECT retained_until FROM run_retention WHERE run_id=runs.id),fork_json FROM runs` + where + ` ORDER BY created_at DESC,id LIMIT ? OFFSET ?`
	pageArgs := append(append([]any{}, args...), limit, offset)
	rows, err := s.db.Query(query, pageArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	page := []*state.Run{}
	runIDs := []string{}
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		page = append(page, run)
		runIDs = append(runIDs, run.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	attemptsByRun := map[string][]state.ExecutionAttempt{}
	for _, id := range runIDs {
		attempts, err := s.listExecutionAttemptPage(id, "", 0, -1, false, "")
		if err != nil {
			return nil, err
		}
		for i := range attempts {
			attempts[i].Result = nil
			attempts[i].OperationRef = nil
			attempts[i].Failure = ""
		}
		if q.Executor != "" {
			attempts = filterAttemptsByExecutorSQLite(attempts, q.Executor)
		}
		attemptsByRun[id] = attempts
	}
	return &state.AnalyticsSnapshot{
		Runs:          page,
		AttemptsByRun: attemptsByRun,
		TotalRuns:     total,
		StateCounts:   counts,
		Offset:        offset,
		Limit:         limit,
		HasMore:       offset+len(page) < total,
		Populations:   state.DefaultAnalyticsPopulations(),
	}, nil
}

func stateSplitCSV(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func filterAttemptsByExecutorSQLite(attempts []state.ExecutionAttempt, executor string) []state.ExecutionAttempt {
	needle := strings.ToLower(executor)
	out := make([]state.ExecutionAttempt, 0, len(attempts))
	for _, a := range attempts {
		if strings.Contains(strings.ToLower(a.Executor), needle) {
			out = append(out, a)
		}
	}
	return out
}
