package sqlite

import (
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/muxlio/muxlio-core/internal/state"
)

func (s *Store) GetSessionQueue(runID, sessionID string) (*state.SessionQueue, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db == nil {
		return nil, errors.New("store closed")
	}
	var raw []byte
	if err := s.db.QueryRow("SELECT queue_json FROM session_input_queues WHERE run_id=? AND session_id=?", runID, sessionID).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, state.ErrSessionQueueNotFound
		}
		return nil, err
	}
	var q state.SessionQueue
	err := json.Unmarshal(raw, &q)
	return &q, err
}
func (s *Store) CompareAndSwapSessionQueue(q *state.SessionQueue, revision int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return false, errors.New("store closed")
	}
	clone := *q
	clone.Revision = revision + 1
	raw, err := json.Marshal(clone)
	if err != nil {
		return false, err
	}
	if revision == 0 {
		result, err := s.db.Exec("INSERT INTO session_input_queues(run_id,session_id,revision,queue_json) VALUES(?,?,?,?) ON CONFLICT DO NOTHING", q.RunID, q.Handle.ID, clone.Revision, raw)
		if err != nil {
			return false, err
		}
		n, err := result.RowsAffected()
		return n == 1, err
	}
	result, err := s.db.Exec("UPDATE session_input_queues SET revision=?,queue_json=? WHERE run_id=? AND session_id=? AND revision=?", clone.Revision, raw, q.RunID, q.Handle.ID, revision)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func (s *Store) AssignmentRequest(id string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db == nil {
		return nil, errors.New("store closed")
	}
	var raw []byte
	err := s.db.QueryRow("SELECT request_json FROM dispatches WHERE assignment_id=?", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return raw, err
}
