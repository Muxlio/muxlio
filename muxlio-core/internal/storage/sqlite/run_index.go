package sqlite

import (
	"github.com/muxlio/muxlio-core/internal/state"
)

func (s *Store) ListRunPage(offset, limit int, search, status, order string, filters ...state.RunFilter) ([]*state.Run, int, map[string]int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	scope := ` WHERE 1=1`
	scopeArgs := []any{}
	if len(filters) > 0 {
		f := filters[0]
		for _, field := range []struct{ column, value string }{
			{"COALESCE(workspace_id,json_extract(NULLIF(plan_json,''),'$.workspaceId'),'')", f.WorkspaceID},
			{"plan_digest", f.PlanDigest}, {"caller_id", f.CallerID},
		} {
			if field.value != "" {
				scope += " AND " + field.column + "=?"
				scopeArgs = append(scopeArgs, field.value)
			}
		}
		// Run timestamps are persisted in UTC with optional fractional digits.
		// Pad those digits for exact nanosecond comparisons, including whole seconds.
		created := runTimestamp("created_at")
		const layout = "2006-01-02T15:04:05.000000000Z"
		if !f.CreatedAfter.IsZero() {
			scope += " AND " + created + ">=?"
			scopeArgs = append(scopeArgs, f.CreatedAfter.UTC().Format(layout))
		}
		if !f.CreatedBefore.IsZero() {
			scope += " AND " + created + "<?"
			scopeArgs = append(scopeArgs, f.CreatedBefore.UTC().Format(layout))
		}
	}
	where := scope + ` AND instr(lower(id||' '||workflow_name),lower(?))>0 AND (?='' OR state=?)`
	args := append(append([]any{}, scopeArgs...), search, status, status)
	var total int
	if err := s.db.QueryRow(`SELECT count(*) FROM runs`+where, args...).Scan(&total); err != nil {
		return nil, 0, nil, err
	}
	counts := map[string]int{}
	rows, err := s.db.Query(`SELECT state,count(*) FROM runs`+scope+` GROUP BY state`, scopeArgs...)
	if err != nil {
		return nil, 0, nil, err
	}
	for rows.Next() {
		var key string
		var count int
		if err := rows.Scan(&key, &count); err != nil {
			rows.Close()
			return nil, 0, nil, err
		}
		counts[key] = count
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, 0, nil, err
	}
	sortOrder := runTimestamp("updated_at") + " DESC,id"
	if order == "created" {
		sortOrder = runTimestamp("created_at") + " DESC,id"
	}
	if order == "workflow" {
		sortOrder = "workflow_name,id"
	}
	query := `SELECT id,workflow_name,state,NULL,NULL,error,created_at,updated_at,NULL,plan_digest,EXISTS(SELECT 1 FROM retention_pins WHERE kind='run' AND retention_pins.id=runs.id),(SELECT retained_until FROM run_retention WHERE run_id=runs.id),fork_json,COALESCE(workspace_id,json_extract(NULLIF(plan_json,''),'$.workspaceId'),''),origin_label,caller_id FROM runs` + where + ` ORDER BY ` + sortOrder + ` LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err = s.db.Query(query, args...)
	if err != nil {
		return nil, 0, nil, err
	}
	defer rows.Close()
	result := []*state.Run{}
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, 0, nil, err
		}
		result = append(result, run)
	}
	return result, total, counts, rows.Err()
}

// column is an internal SQL column name, never request text.
func runTimestamp(column string) string {
	return "(substr(" + column + ",1,19)||'.'||substr(CASE WHEN substr(" + column + ",20,1)='.' THEN substr(" + column + ",21,length(" + column + ")-21) ELSE '' END||'000000000',1,9)||'Z')"
}
