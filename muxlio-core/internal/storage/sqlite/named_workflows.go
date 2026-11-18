package sqlite

import (
	"database/sql"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
)

func (s *Store) PublishWorkflow(id string, expectedRevision int64, publication state.WorkflowPublication) (state.NamedWorkflow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return state.NamedWorkflow{}, err
	}
	defer tx.Rollback()
	// Acquire SQLite's write lock before reading the revision so independent
	// connections serialize the check and the change.
	if _, err := tx.Exec(`UPDATE named_workflows SET current_revision=current_revision WHERE id=?`, id); err != nil {
		return state.NamedWorkflow{}, err
	}
	workflow, err := readNamedWorkflow(tx, id)
	if err != nil {
		return state.NamedWorkflow{}, err
	}
	previous := workflow.CurrentRevision
	added, err := workflow.ApplyPublication(expectedRevision, publication)
	if err != nil {
		return state.NamedWorkflow{}, err
	}
	if previous == workflow.CurrentRevision {
		return workflow.CurrentFirst(), nil
	}
	if added {
		retired, err := saveWorkflowVersion(tx, publication.Digest, publication.PlanJSON)
		if err != nil {
			return state.NamedWorkflow{}, err
		}
		if retired {
			return state.NamedWorkflow{}, state.ErrWorkflowVersionRetired
		}
	}
	if _, err := tx.Exec(`INSERT INTO named_workflows (id, current_digest, current_revision) VALUES (?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET current_digest=excluded.current_digest, current_revision=excluded.current_revision`,
		id, workflow.CurrentDigest, workflow.CurrentRevision); err != nil {
		return state.NamedWorkflow{}, err
	}
	if added {
		if _, err := tx.Exec(`INSERT INTO named_workflow_versions (workflow_id, digest, publication_revision, published_at)
			VALUES (?, ?, ?, ?)`, id, publication.Digest, workflow.CurrentRevision, rfc3339(workflow.Versions[0].PublishedAt)); err != nil {
			return state.NamedWorkflow{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return state.NamedWorkflow{}, err
	}
	return workflow.CurrentFirst(), nil
}

func (s *Store) GetNamedWorkflow(id string) (state.NamedWorkflow, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	workflow, err := readNamedWorkflow(s.db, id)
	if err != nil {
		return state.NamedWorkflow{}, err
	}
	if workflow.CurrentRevision == 0 {
		return state.NamedWorkflow{}, state.ErrNamedWorkflowNotFound
	}
	return workflow.CurrentFirst(), nil
}

func readNamedWorkflow(db interface {
	Query(string, ...any) (*sql.Rows, error)
}, id string) (state.NamedWorkflow, error) {
	workflow := state.NamedWorkflow{ID: id}
	rows, err := db.Query(`SELECT w.current_digest, w.current_revision, h.digest,
		COALESCE(json_extract(CAST(p.plan_json AS TEXT), '$.name'), ''), h.published_at, p.retired
		FROM named_workflows w JOIN named_workflow_versions h ON w.id=h.workflow_id
		JOIN workflow_versions p ON h.digest=p.digest
		WHERE w.id=? ORDER BY h.publication_revision DESC`, id)
	if err != nil {
		return state.NamedWorkflow{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var version state.NamedWorkflowVersion
		var publishedAt string
		if err := rows.Scan(&workflow.CurrentDigest, &workflow.CurrentRevision, &version.Digest,
			&version.Name, &publishedAt, &version.Retired); err != nil {
			return state.NamedWorkflow{}, err
		}
		version.PublishedAt, err = time.Parse(time.RFC3339Nano, publishedAt)
		if err != nil {
			return state.NamedWorkflow{}, err
		}
		workflow.Versions = append(workflow.Versions, version)
	}
	return workflow, rows.Err()
}
