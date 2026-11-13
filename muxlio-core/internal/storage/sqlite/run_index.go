package sqlite

import (
	"github.com/muxlio/muxlio-core/internal/state"
)

func (s *Store) ListRunPage(offset, limit int, search, status, order string) ([]*state.Run, int, map[string]int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	where := ` WHERE instr(lower(id||' '||workflow_name),lower(?))>0 AND (?='' OR state=?)`
	args := []any{search, status, status}
	var total int
	if err := s.db.QueryRow(`SELECT count(*) FROM runs`+where, args...).Scan(&total); err != nil {
		return nil, 0, nil, err
	}
	counts := map[string]int{}
	rows, err := s.db.Query(`SELECT state,count(*) FROM runs GROUP BY state`)
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
	sortOrder := "updated_at DESC,id"
	if order == "created" {
		sortOrder = "created_at DESC,id"
	}
	if order == "workflow" {
		sortOrder = "workflow_name,id"
	}
	query := `SELECT id,workflow_name,state,NULL,NULL,error,created_at,updated_at,NULL,plan_digest,EXISTS(SELECT 1 FROM retention_pins WHERE kind='run' AND retention_pins.id=runs.id),(SELECT retained_until FROM run_retention WHERE run_id=runs.id),fork_json,COALESCE(workspace_id,json_extract(NULLIF(plan_json,''),'$.workspaceId'),'') FROM runs` + where + ` ORDER BY ` + sortOrder + ` LIMIT ? OFFSET ?`
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
