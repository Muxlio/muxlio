package sqlite

import (
	"database/sql"
	"encoding/json"
	"github.com/muxlio/muxlio-core/internal/state"
	"time"
)

var _ state.RunViewStore = (*Store)(nil)

func (s *Store) ActivationCounts(runID string) (total, completed int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	err = s.db.QueryRow(`SELECT count(*), coalesce(sum(state='succeeded'),0) FROM activations WHERE run_id=?`, runID).Scan(&total, &completed)
	return
}

func (s *Store) ListOutputPage(runID string, offset, limit int, search string) ([]state.OutputEntry, error) {
	return s.listOutputPage(runID, offset, limit, search, false)
}

func (s *Store) ListArtifactReferencePage(runID string, offset, limit int) ([]state.OutputEntry, error) {
	return s.listOutputPage(runID, offset, limit, "", true)
}

func (s *Store) listOutputPage(runID string, offset, limit int, search string, referencesOnly bool) ([]state.OutputEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`WITH completions AS (SELECT run_id,subject_id,max(seq) AS seq FROM events WHERE type='activation.succeeded' AND (?='' OR run_id=?) GROUP BY run_id,subject_id),
 ports AS (SELECT a.id,a.run_id,a.node_id,a.rowid AS position,coalesce(c.seq,0) AS completed,j.key,
 coalesce(json_extract(CASE WHEN j.type='object' THEN j.value ELSE '{}' END,'$.digest'),'') AS digest,
 coalesce(json_extract(CASE WHEN j.type='object' THEN j.value ELSE '{}' END,'$.mime'),'') AS mime,
 coalesce(json_extract(CASE WHEN j.type='object' THEN j.value ELSE '{}' END,'$.path'),'') AS path,
 coalesce(json_extract(CASE WHEN j.type='object' THEN j.value ELSE '{}' END,'$.size'),0) AS size
 FROM activations a LEFT JOIN completions c ON c.run_id=a.run_id AND c.subject_id=a.id,json_each(a.outputs_json) j WHERE (?='' OR a.run_id=?))
 SELECT id,run_id,node_id,key,digest,mime,path,size FROM ports WHERE (?<>'' OR digest LIKE 'sha256:%') AND (NOT ? OR substr(digest,1,7)='sha256:') AND instr(lower(run_id||' '||node_id||' '||key||' '||digest||' '||path||' '||mime),lower(?))>0 ORDER BY completed DESC,position DESC,key LIMIT ? OFFSET ?`, runID, runID, runID, runID, runID, referencesOnly, search, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []state.OutputEntry{}
	for rows.Next() {
		var entry state.OutputEntry
		if err := rows.Scan(&entry.ActivationID, &entry.RunID, &entry.NodeID, &entry.Name, &entry.Digest, &entry.Mime, &entry.Path, &entry.Size); err != nil {
			return nil, err
		}
		result = append(result, entry)
	}
	return result, rows.Err()
}

func (s *Store) SnapshotPage(runID, activationID, attemptID string, after, limit int, current bool) ([]state.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	query := `SELECT e.run_id,e.seq,e.type,e.subject_id,e.payload_json,e.timestamp FROM events e WHERE e.run_id=? AND e.type='artifact.snapshot' AND e.seq>?`
	args := []any{runID, after}
	if activationID != "" {
		query += ` AND json_extract(e.payload_json,'$.activationId')=?`
		args = append(args, activationID)
	}
	if attemptID != "" {
		query += ` AND e.subject_id=?`
		args = append(args, attemptID)
	}
	if current {
		query += ` AND NOT EXISTS (SELECT 1 FROM events n WHERE n.run_id=e.run_id AND n.type=e.type AND n.seq>e.seq AND json_extract(n.payload_json,'$.activationId')=json_extract(e.payload_json,'$.activationId') AND json_extract(n.payload_json,'$.snapshot.name')=json_extract(e.payload_json,'$.snapshot.name')`
		if attemptID != "" {
			query += ` AND n.subject_id=?`
			args = append(args, attemptID)
		}
		query += `)`
	}
	query += ` ORDER BY e.seq LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []state.Event{}
	for rows.Next() {
		var e state.Event
		var raw, stamp string
		if err := rows.Scan(&e.RunID, &e.Seq, &e.Type, &e.SubjectID, &raw, &stamp); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &e.Payload); err != nil {
			return nil, err
		}
		e.Timestamp, _ = time.Parse(time.RFC3339Nano, stamp)
		result = append(result, e)
	}
	return result, rows.Err()
}

func (s *Store) LatestEventSeq(id string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var seq int
	err := s.db.QueryRow(`SELECT coalesce(max(seq),0) FROM events WHERE run_id=?`, id).Scan(&seq)
	return seq, err
}

func (s *Store) GetAttempt(runID, id string) (*state.ExecutionAttempt, error) {
	items, err := s.listExecutionAttemptPage(runID, "", 0, 1, true, id)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, sql.ErrNoRows
	}
	return &items[0], nil
}

func (s *Store) GetRunHeader(id string) (*state.Run, error) { return s.getRun(id, false) }
func (s *Store) ActiveActivationOffset(id string, limit int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var before int
	err := s.db.QueryRow(`SELECT count(*) FROM activations WHERE run_id=? AND rowid<(SELECT rowid FROM activations WHERE run_id=? ORDER BY CASE state WHEN 'running' THEN 0 WHEN 'waiting' THEN 1 WHEN 'queued' THEN 2 WHEN 'failed' THEN 3 ELSE 4 END,rowid DESC LIMIT 1)`, id, id).Scan(&before)
	return before / limit * limit, err
}

func (s *Store) RecentEvents(id string, limit int) ([]state.Event, error) {
	items, err := s.EventsBefore(id, 0, limit)
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}
	return items, err
}

func (s *Store) EventsBefore(id string, before, limit int) ([]state.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT run_id,seq,type,subject_id,payload_json,timestamp FROM events WHERE run_id=? AND (?=0 OR seq<?) ORDER BY seq DESC LIMIT ?`, id, before, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []state.Event{}
	for rows.Next() {
		var event state.Event
		var raw sql.NullString
		var stamp string
		if err := rows.Scan(&event.RunID, &event.Seq, &event.Type, &event.SubjectID, &raw, &stamp); err != nil {
			return nil, err
		}
		event.Timestamp, _ = time.Parse(time.RFC3339Nano, stamp)
		if raw.Valid {
			if err := json.Unmarshal([]byte(raw.String), &event.Payload); err != nil {
				return nil, err
			}
		}
		items = append(items, event)
	}
	return items, rows.Err()
}

func (s *Store) RunLifecycleTimes(id string) (*time.Time, *time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT type,timestamp FROM events WHERE run_id=? AND type IN ('run.running','run.succeeded','run.failed','run.cancelled') ORDER BY seq`, id)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var start, finish *time.Time
	for rows.Next() {
		var kind string
		var raw string
		if err := rows.Scan(&kind, &raw); err != nil {
			return nil, nil, err
		}
		t, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return nil, nil, err
		}
		if kind == state.EventRunRunning {
			if start == nil {
				start = &t
			}
		} else {
			finish = &t
		}
	}
	return start, finish, rows.Err()
}
