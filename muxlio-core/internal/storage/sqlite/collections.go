package sqlite

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
)

func (s *Store) UpdateRunCollection(runID string, spec state.CollectionSpec, action state.CollectionAction) (state.RunCollection, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return state.RunCollection{}, false, err
	}
	defer tx.Rollback()

	var runState string
	var raw []byte
	err = tx.QueryRow(`SELECT runs.state, collection_json FROM runs
		LEFT JOIN run_collections ON run_collections.run_id = runs.id AND run_collections.name = ?
		WHERE runs.id = ?`, spec.Seed.Name, runID).Scan(&runState, &raw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = errors.New("muxlio: run not found")
		}
		return state.RunCollection{}, false, err
	}

	collection := spec.Seed
	if raw != nil {
		collection = state.RunCollection{}
		if err := json.Unmarshal(raw, &collection); err != nil {
			return state.RunCollection{}, false, err
		}
	}
	now := time.Now()
	changed, accepted, actionErr := action.Apply(&collection, spec, runState, now)
	if changed || raw == nil {
		encoded, err := json.Marshal(collection)
		if err != nil {
			return state.RunCollection{}, false, err
		}
		if _, err := tx.Exec(`INSERT INTO run_collections(run_id, name, collection_json) VALUES(?, ?, ?)
			ON CONFLICT(run_id, name) DO UPDATE SET collection_json=excluded.collection_json`, runID, collection.Name, encoded); err != nil {
			return state.RunCollection{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return state.RunCollection{}, false, err
	}
	return collection.View(spec, runState, now), accepted, actionErr
}
