// Package sqlite implements the durable coordinator Store using SQLite WAL.
// Current-state rows and the matching history event are committed in one
// transaction so a restart cannot observe a completed event with a stale row.
package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/coordinatorlog"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
	_ "modernc.org/sqlite"
)

var schemaStmts = []string{
	`CREATE TABLE IF NOT EXISTS subscriptions (id TEXT PRIMARY KEY, definition_json BLOB NOT NULL, secret TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS subscription_deliveries (id TEXT PRIMARY KEY, subscription_id TEXT NOT NULL, state TEXT NOT NULL, due INTEGER NOT NULL, record_json BLOB NOT NULL)`,
	`CREATE INDEX IF NOT EXISTS idx_subscription_due ON subscription_deliveries(state, due)`,
	`CREATE TABLE IF NOT EXISTS subscription_cursors (id TEXT PRIMARY KEY, seq INTEGER NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS schedules (id TEXT PRIMARY KEY, definition_json BLOB NOT NULL, plan_json BLOB NOT NULL, next_fire TEXT)`,
	`CREATE TABLE IF NOT EXISTS schedule_fires (schedule_id TEXT NOT NULL, due_at TEXT NOT NULL, through_at TEXT, state TEXT NOT NULL, run_id TEXT, PRIMARY KEY(schedule_id, due_at), FOREIGN KEY(schedule_id) REFERENCES schedules(id), FOREIGN KEY(run_id) REFERENCES runs(id))`,
	`CREATE TABLE IF NOT EXISTS session_input_queues (run_id TEXT NOT NULL, session_id TEXT NOT NULL, revision INTEGER NOT NULL, queue_json BLOB NOT NULL, PRIMARY KEY(run_id, session_id), FOREIGN KEY(run_id) REFERENCES runs(id))`,
	`CREATE TABLE IF NOT EXISTS exclusive_resources (resource_key TEXT PRIMARY KEY, attempt_id TEXT NOT NULL, activation_id TEXT NOT NULL)`,
	`PRAGMA journal_mode = WAL`,
	`PRAGMA busy_timeout = 5000`,
	`PRAGMA foreign_keys = ON`,
	`PRAGMA synchronous = NORMAL`,
	`CREATE TABLE IF NOT EXISTS admission_config (id INTEGER PRIMARY KEY CHECK(id=1), config_json TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS admission_reservations (activation_id TEXT PRIMARY KEY, slots INTEGER NOT NULL, executor TEXT NOT NULL, operator_key TEXT NOT NULL, provider_key TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS admission_turns (run_id TEXT PRIMARY KEY, turn INTEGER NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS admission_buckets (provider_key TEXT PRIMARY KEY, tokens REAL NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS admission_queue (activation_id TEXT PRIMARY KEY, reason TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS runs (
  id TEXT PRIMARY KEY,
  workflow_name TEXT NOT NULL,
  state TEXT NOT NULL,
  input_json TEXT,
  output_json TEXT,
  error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
)`,
	`CREATE TABLE IF NOT EXISTS activations (
  id TEXT PRIMARY KEY,
  run_id TEXT NOT NULL,
  scope_path_json TEXT NOT NULL DEFAULT '[]',
  node_id TEXT NOT NULL,
  occurrence INTEGER NOT NULL DEFAULT 0,
  attempt_base INTEGER NOT NULL DEFAULT 0,
  state TEXT NOT NULL,
  outputs_json TEXT,
  error TEXT NOT NULL DEFAULT '',
  UNIQUE(run_id, scope_path_json, node_id, occurrence),
  FOREIGN KEY(run_id) REFERENCES runs(id)
)`,
	`CREATE TABLE IF NOT EXISTS events (
  run_id TEXT NOT NULL,
  seq INTEGER NOT NULL,
  timestamp TEXT NOT NULL,
  type TEXT NOT NULL,
  subject_id TEXT NOT NULL DEFAULT '',
  payload_json TEXT,
  PRIMARY KEY(run_id, seq),
  FOREIGN KEY(run_id) REFERENCES runs(id)
)`,
	`CREATE INDEX IF NOT EXISTS idx_activations_run ON activations(run_id)`,
	`CREATE INDEX IF NOT EXISTS idx_events_run ON events(run_id, seq)`,
	`CREATE INDEX IF NOT EXISTS idx_events_subject_type ON events(run_id, subject_id, type)`,
	`CREATE TABLE IF NOT EXISTS assignments (activation_id TEXT PRIMARY KEY, run_id TEXT NOT NULL, worker_id TEXT NOT NULL, generation INTEGER NOT NULL, lease_until TEXT NOT NULL, state TEXT NOT NULL, assignment_id TEXT NOT NULL DEFAULT '', attempt_id TEXT NOT NULL DEFAULT '', FOREIGN KEY(run_id) REFERENCES runs(id))`,
	`CREATE TABLE IF NOT EXISTS workers (id TEXT PRIMARY KEY, capacity INTEGER NOT NULL, capabilities_json TEXT NOT NULL, labels_json TEXT NOT NULL DEFAULT '{}', last_seen TEXT NOT NULL, draining INTEGER NOT NULL DEFAULT 0)`,
	`CREATE TABLE IF NOT EXISTS dispatches (assignment_id TEXT PRIMARY KEY, run_id TEXT NOT NULL, activation_id TEXT NOT NULL UNIQUE, attempt_id TEXT NOT NULL, executor TEXT NOT NULL, request_json TEXT NOT NULL, state TEXT NOT NULL DEFAULT 'pending', FOREIGN KEY(run_id) REFERENCES runs(id))`,
	`CREATE TABLE IF NOT EXISTS worker_results (attempt_id TEXT PRIMARY KEY, result_json BLOB NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS worker_diagnostics (id INTEGER PRIMARY KEY AUTOINCREMENT, attempt_id TEXT NOT NULL, payload_json BLOB NOT NULL, recorded_at TEXT NOT NULL)`,
	`CREATE INDEX IF NOT EXISTS idx_worker_diagnostics_attempt ON worker_diagnostics(attempt_id,id)`,
	`CREATE TABLE IF NOT EXISTS coordinator_meta (key TEXT PRIMARY KEY, value INTEGER NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS timers (id TEXT PRIMARY KEY, run_id TEXT NOT NULL, activation_id TEXT NOT NULL DEFAULT '', due_at TEXT NOT NULL, state TEXT NOT NULL DEFAULT 'pending', payload_json TEXT, FOREIGN KEY(run_id) REFERENCES runs(id))`,
	`CREATE INDEX IF NOT EXISTS idx_timers_due ON timers(state, due_at)`,
	`CREATE TABLE IF NOT EXISTS signals (run_id TEXT NOT NULL, name TEXT NOT NULL, seq INTEGER NOT NULL, payload_json TEXT, timestamp TEXT NOT NULL, PRIMARY KEY(run_id,name,seq), FOREIGN KEY(run_id) REFERENCES runs(id))`,
	`CREATE TABLE IF NOT EXISTS input_requests (run_id TEXT NOT NULL, request_id TEXT NOT NULL, activation_id TEXT NOT NULL DEFAULT '', name TEXT NOT NULL, schema_json TEXT, expires_at TEXT, permissions_json TEXT NOT NULL DEFAULT '[]', state TEXT NOT NULL DEFAULT 'pending', response_json TEXT, responder_id TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, fulfilled_at TEXT, idempotency_key TEXT NOT NULL DEFAULT '', PRIMARY KEY(run_id, request_id), FOREIGN KEY(run_id) REFERENCES runs(id))`,
	`CREATE INDEX IF NOT EXISTS idx_input_requests_run ON input_requests(run_id, state)`,
	`CREATE TABLE IF NOT EXISTS log_chunks (run_id TEXT NOT NULL, activation_id TEXT NOT NULL, stream TEXT NOT NULL, seq INTEGER NOT NULL, data BLOB NOT NULL, timestamp TEXT NOT NULL, PRIMARY KEY(activation_id,stream,seq), FOREIGN KEY(run_id) REFERENCES runs(id))`,
	`CREATE TABLE IF NOT EXISTS execution_attempts (id TEXT PRIMARY KEY, run_id TEXT NOT NULL, activation_id TEXT NOT NULL, retry_number INTEGER NOT NULL, executor TEXT NOT NULL, state TEXT NOT NULL, started_at TEXT NOT NULL, finished_at TEXT NOT NULL, failure TEXT NOT NULL DEFAULT '', usage_json TEXT, execution_duration_ms INTEGER, FOREIGN KEY(run_id) REFERENCES runs(id))`,
	`CREATE TABLE IF NOT EXISTS worker_execution_timings (attempt_id TEXT NOT NULL, assignment_id TEXT NOT NULL, generation INTEGER NOT NULL, run_id TEXT NOT NULL, activation_id TEXT NOT NULL, worker_id TEXT NOT NULL, duration_ms INTEGER CHECK(duration_ms>=0), PRIMARY KEY(attempt_id,assignment_id,generation))`,
	`CREATE INDEX IF NOT EXISTS idx_execution_attempts_run ON execution_attempts(run_id, activation_id, retry_number)`,
	`CREATE TABLE IF NOT EXISTS outbox (id TEXT PRIMARY KEY, kind TEXT NOT NULL, payload_json TEXT NOT NULL, created_at TEXT NOT NULL, published_at TEXT)`,
	`CREATE INDEX IF NOT EXISTS idx_outbox_unpublished ON outbox(published_at, created_at)`,
	`CREATE TABLE IF NOT EXISTS workflow_versions (digest TEXT PRIMARY KEY, plan_json BLOB NOT NULL, created_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS triggers (id TEXT PRIMARY KEY, definition_json BLOB NOT NULL, secret TEXT NOT NULL, created_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS trigger_deliveries (id INTEGER PRIMARY KEY AUTOINCREMENT, trigger_id TEXT NOT NULL, idempotency_key TEXT NOT NULL DEFAULT '', run_id TEXT, workflow_digest TEXT NOT NULL DEFAULT '', state TEXT NOT NULL, reason TEXT NOT NULL DEFAULT '', timestamp TEXT NOT NULL, FOREIGN KEY(trigger_id) REFERENCES triggers(id), FOREIGN KEY(run_id) REFERENCES runs(id))`,
	`CREATE INDEX IF NOT EXISTS idx_trigger_deliveries_dedup ON trigger_deliveries(trigger_id, idempotency_key, state, timestamp)`,
	`CREATE INDEX IF NOT EXISTS idx_trigger_deliveries_run ON trigger_deliveries(run_id)`,
}

var _ state.Store = (*Store)(nil)

// Store is a SQLite-backed state.Store.
type Store struct {
	db *sql.DB
	mu sync.RWMutex
}

// Open opens or creates a coordinator database at path.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("muxlio: creating data dir: %w", err)
	}
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("muxlio: opening sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	for _, stmt := range schemaStmts {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			return nil, fmt.Errorf("muxlio: applying sqlite schema: %w", err)
		}
	}
	// Older databases predate the assignment identity columns.
	for _, stmt := range []string{
		`ALTER TABLE runs ADD COLUMN plan_json BLOB`,
		`ALTER TABLE runs ADD COLUMN plan_digest TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE assignments ADD COLUMN assignment_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE assignments ADD COLUMN attempt_id TEXT NOT NULL DEFAULT ''`,
		// Signal idempotency key: allows callers to deduplicate retried deliveries.
		`ALTER TABLE signals ADD COLUMN idempotency_key TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE workers ADD COLUMN instance_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE workers ADD COLUMN labels_json TEXT NOT NULL DEFAULT '{}'`,
		// Attempt-level token usage reported by the executor/adapter.
		`ALTER TABLE execution_attempts ADD COLUMN usage_json TEXT`,
		`ALTER TABLE execution_attempts ADD COLUMN measurements_json TEXT`,
		`ALTER TABLE execution_attempts ADD COLUMN result_json TEXT`,
		`ALTER TABLE execution_attempts ADD COLUMN execution_duration_ms INTEGER`,
		`ALTER TABLE activations ADD COLUMN attempt_base INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE activations ADD COLUMN inputs_json BLOB`,
		// Drain mode: worker is finishing current assignments but takes no new ones.
		`ALTER TABLE workers ADD COLUMN draining INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE assignments ADD COLUMN coordinator_epoch INTEGER NOT NULL DEFAULT 0`,
		// Effect policy classification for each attempt; empty means unclassified.
		`ALTER TABLE execution_attempts ADD COLUMN effect TEXT NOT NULL DEFAULT ''`,
		// Stable idempotency key for idempotency-key effect attempts.
		`ALTER TABLE execution_attempts ADD COLUMN idempotency_key TEXT NOT NULL DEFAULT ''`,
		// Adapter-returned operation reference (JSON), for reconciliation.
		`ALTER TABLE execution_attempts ADD COLUMN operation_ref_json TEXT`,
		// Reason an attempt is held pending explicit operator reconciliation.
		`ALTER TABLE execution_attempts ADD COLUMN await_reconciliation_reason TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := db.Exec(stmt); err != nil && !isDuplicateColumn(err) {
			db.Close()
			return nil, fmt.Errorf("muxlio: migrating sqlite schema: %w", err)
		}
	}
	return &Store{db: db}, nil
}

func isDuplicateColumn(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "duplicate column name") || strings.Contains(err.Error(), "already exists"))
}

func decodeWorkerCapabilities(raw string) ([]state.WorkerCapability, error) {
	var caps []state.WorkerCapability
	if err := json.Unmarshal([]byte(raw), &caps); err == nil {
		if len(caps) == 0 {
			return caps, nil
		}
		if caps[0].Name != "" {
			return caps, nil
		}
	}
	var names []string
	if err := json.Unmarshal([]byte(raw), &names); err != nil {
		return nil, err
	}
	caps = make([]state.WorkerCapability, len(names))
	for i, name := range names {
		caps[i] = state.WorkerCapability{Name: name}
	}
	return caps, nil
}

// CoordinatorEpoch advances once for every coordinator process using this database.
func (s *Store) CoordinatorEpoch() (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO coordinator_meta(key,value) VALUES('epoch',1) ON CONFLICT(key) DO UPDATE SET value=value+1`)
	if err != nil {
		return 0, err
	}
	var epoch int64
	err = s.db.QueryRow(`SELECT value FROM coordinator_meta WHERE key='epoch'`).Scan(&epoch)
	return epoch, err
}

// StartCoordinator advances the fencing epoch and reconciles leases from the
// previous process. Pure requests can replay; all other requests remain held
// for manual reconciliation because their side effects are unknowable.
func (s *Store) StartCoordinator() (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO coordinator_meta(key,value) VALUES('epoch',1) ON CONFLICT(key) DO UPDATE SET value=value+1`); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(`UPDATE assignments SET state='uncertain' WHERE state='active'`); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(`UPDATE dispatches SET state='uncertain' WHERE state='assigned' AND activation_id IN (SELECT activation_id FROM assignments WHERE state='uncertain')`); err != nil {
		return 0, err
	}
	if err = recoverLostDispatches(tx); err != nil {
		return 0, err
	}
	var epoch int64
	if err = tx.QueryRow(`SELECT value FROM coordinator_meta WHERE key='epoch'`).Scan(&epoch); err != nil {
		return 0, err
	}
	return epoch, tx.Commit()
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

// ClaimAssignment atomically advances the fencing generation for an activation.
func (s *Store) ClaimAssignment(a state.Assignment) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var generation int64
	err = tx.QueryRow(`SELECT generation FROM assignments WHERE activation_id=?`, a.ActivationID).Scan(&generation)
	if err != nil && err != sql.ErrNoRows {
		return 0, err
	}
	generation++
	_, err = tx.Exec(`INSERT INTO assignments(activation_id,run_id,worker_id,generation,lease_until,state,assignment_id,attempt_id) VALUES(?,?,?,?,?,'active',?,?) ON CONFLICT(activation_id) DO UPDATE SET run_id=excluded.run_id,worker_id=excluded.worker_id,generation=excluded.generation,lease_until=excluded.lease_until,state='active',assignment_id=excluded.assignment_id,attempt_id=excluded.attempt_id`, a.ActivationID, a.RunID, a.WorkerID, generation, rfc3339(a.LeaseUntil), a.AssignmentID, a.AttemptID)
	if err != nil {
		return 0, err
	}
	if err := checkCoordinatorEpoch(tx, a.CoordinatorEpoch); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(`UPDATE assignments SET coordinator_epoch=? WHERE activation_id=?`, a.CoordinatorEpoch, a.ActivationID); err != nil {
		return 0, err
	}
	if err := registerWorkerTiming(tx, a, generation); err != nil {
		return 0, err
	}
	return generation, tx.Commit()
}

// CompleteAssignment accepts a result only from the current fencing generation.
func (s *Store) CompleteAssignment(activationID string, generation int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`UPDATE assignments SET state='completed' WHERE activation_id=? AND generation=? AND state='active'`, activationID, generation)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// CompleteWorkerAssignment atomically validates complete assignment identity
// and lease validity before accepting an acknowledgement.
func (s *Store) CompleteWorkerAssignment(a state.Assignment, terminalState string, now time.Time, result ...[]byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if terminalState != "succeeded" && terminalState != "failed" && terminalState != "cancelled" {
		return false, fmt.Errorf("invalid worker completion state %q", terminalState)
	}
	if err := checkCoordinatorEpoch(tx, a.CoordinatorEpoch); err != nil {
		return false, err
	}
	// Use storage admission time if the caller waited behind another writer.
	if current := time.Now(); current.After(now) {
		now = current
	}
	var requestJSON string
	requestErr := tx.QueryRow(`SELECT d.request_json FROM dispatches d JOIN assignments a ON a.attempt_id=d.attempt_id WHERE a.attempt_id=? AND a.assignment_id=? AND a.worker_id=? AND a.run_id=? AND a.activation_id=? AND a.generation=? AND a.coordinator_epoch=?`, a.AttemptID, a.AssignmentID, a.WorkerID, a.RunID, a.ActivationID, a.Generation, a.CoordinatorEpoch).Scan(&requestJSON)
	if requestErr != nil && requestErr != sql.ErrNoRows {
		return false, requestErr
	}
	if requestErr == nil {
		var req workers.ExecuteRequest
		if err := json.Unmarshal([]byte(requestJSON), &req); err != nil {
			return false, err
		}
		if (req.ExecutionDeadline != nil && !now.Before(*req.ExecutionDeadline)) || (req.Deadline != nil && !now.Before(*req.Deadline)) {
			if _, err := tx.Exec(`UPDATE assignments SET state='cancelled',generation=generation+1 WHERE attempt_id=? AND state='active'`, a.AttemptID); err != nil {
				return false, err
			}
			if _, err := tx.Exec(`UPDATE dispatches SET state='cancelled' WHERE attempt_id=? AND state='assigned'`, a.AttemptID); err != nil {
				return false, err
			}
			return false, tx.Commit()
		}
	}
	var cancelled int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM runs WHERE id=? AND state='cancelled'`, a.RunID).Scan(&cancelled); err != nil {
		return false, err
	}
	if cancelled != 0 {
		return false, nil
	}
	res, err := tx.Exec(`UPDATE assignments SET state=? WHERE activation_id=? AND run_id=? AND worker_id=? AND generation=? AND assignment_id=? AND attempt_id=? AND state='active' AND lease_until>? AND coordinator_epoch=?`, terminalState, a.ActivationID, a.RunID, a.WorkerID, a.Generation, a.AssignmentID, a.AttemptID, rfc3339(now), a.CoordinatorEpoch)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n != 1 {
		var status string
		err = tx.QueryRow(`SELECT state FROM assignments WHERE activation_id=? AND run_id=? AND worker_id=? AND generation=? AND assignment_id=? AND attempt_id=?`, a.ActivationID, a.RunID, a.WorkerID, a.Generation, a.AssignmentID, a.AttemptID).Scan(&status)
		if err == sql.ErrNoRows {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if status != terminalState {
			return false, nil
		}
		var saved []byte
		err = tx.QueryRow(`SELECT result_json FROM worker_results WHERE attempt_id=?`, a.AttemptID).Scan(&saved)
		if err == sql.ErrNoRows {
			return len(result) == 0, nil
		}
		if err != nil {
			return false, err
		}
		return len(result) > 0 && bytes.Equal(saved, result[0]), nil
	}
	if len(result) > 0 {
		if _, err = tx.Exec(`INSERT INTO worker_results(attempt_id,result_json) VALUES(?,?) ON CONFLICT(attempt_id) DO NOTHING`, a.AttemptID, result[0]); err != nil {
			return false, err
		}
	}
	if _, err = tx.Exec(`DELETE FROM exclusive_resources WHERE attempt_id=?`, a.AttemptID); err != nil {
		return false, err
	}
	if _, err = tx.Exec(`UPDATE dispatches SET state='completed' WHERE activation_id=?`, a.ActivationID); err != nil {
		return false, err
	}
	var executor string
	if scanErr := tx.QueryRow(`SELECT executor FROM dispatches WHERE activation_id=?`, a.ActivationID).Scan(&executor); scanErr != nil {
		// Best-effort: the dispatch row may be absent in tests or after a schema
		// migration. Log with an empty executor rather than aborting the commit.
		_ = scanErr
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	coordinatorlog.WorkerCompleted(a.RunID, a.ActivationID, a.WorkerID, executor, a.AttemptID, terminalState)
	return true, nil
}

// RegisterWorker persists the worker's current capacity and capabilities.
func (s *Store) RegisterWorker(w state.Worker) error {
	raw, err := json.Marshal(w.Capabilities)
	if err != nil {
		return err
	}
	labels := w.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	labelsRaw, err := json.Marshal(labels)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var previous string
	err = tx.QueryRow(`SELECT instance_id FROM workers WHERE id=?`, w.ID).Scan(&previous)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	// A new process cannot inherit the old process's leases. Its attempts may
	// have performed effects, so fence their results and retain uncertainty.
	if err == nil && previous != w.InstanceID {
		if _, err = tx.Exec(`UPDATE dispatches SET state='uncertain' WHERE state='assigned' AND assignment_id IN (SELECT assignment_id FROM assignments WHERE worker_id=? AND state='active')`, w.ID); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE assignments SET state='uncertain',generation=generation+1 WHERE worker_id=? AND state='active'`, w.ID); err != nil {
			return err
		}
	}
	if err = recoverLostDispatches(tx); err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO workers(id,instance_id,capacity,capabilities_json,labels_json,last_seen,draining) VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET instance_id=excluded.instance_id,capacity=excluded.capacity,capabilities_json=excluded.capabilities_json,labels_json=excluded.labels_json,last_seen=excluded.last_seen,draining=excluded.draining`, w.ID, w.InstanceID, w.Capacity, string(raw), string(labelsRaw), rfc3339(w.LastSeen), boolToInt(w.Draining))
	if err != nil {
		return err
	}
	return tx.Commit()
}

// ListWorkers returns workers that have sent a heartbeat within the given duration.
func (s *Store) ListWorkers(since time.Time) ([]state.Worker, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT id,instance_id,capacity,capabilities_json,labels_json,last_seen,draining FROM workers WHERE last_seen>=? ORDER BY id`, rfc3339(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []state.Worker
	for rows.Next() {
		var w state.Worker
		var raw, labelsRaw, seen string
		var draining int
		if err := rows.Scan(&w.ID, &w.InstanceID, &w.Capacity, &raw, &labelsRaw, &seen, &draining); err != nil {
			return nil, err
		}
		w.Capabilities, err = decodeWorkerCapabilities(raw)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(labelsRaw), &w.Labels); err != nil {
			return nil, err
		}
		w.LastSeen, err = time.Parse(time.RFC3339Nano, seen)
		if err != nil {
			return nil, err
		}
		w.Draining = draining != 0
		result = append(result, w)
	}
	return result, rows.Err()
}

// EnqueueDispatch durably records an execute envelope for worker polling.
func (s *Store) EnqueueDispatch(d state.Dispatch) error {
	var request workers.ExecuteRequest
	if err := json.Unmarshal(d.Request, &request); err != nil {
		return err
	}
	if err := workers.ValidateWorkspaceContract(request.WorkspaceMode, request.ConcurrencyKeys); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO dispatches(assignment_id,run_id,activation_id,attempt_id,executor,request_json,state) VALUES(?,?,?,?,?,?,'pending') ON CONFLICT(activation_id) DO UPDATE SET assignment_id=excluded.assignment_id,attempt_id=excluded.attempt_id,executor=excluded.executor,request_json=excluded.request_json,state='pending' WHERE dispatches.attempt_id<>excluded.attempt_id AND dispatches.state='completed'`, d.AssignmentID, d.RunID, d.ActivationID, d.AttemptID, d.Executor, string(d.Request))
	return err
}

// UnclaimedDispatch proves a request never reached a worker. An expired,
// completed or uncertain assignment prevents automatic interpreter replay.
func (s *Store) UnclaimedDispatch(activationID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM dispatches WHERE activation_id=? AND state='pending' AND NOT EXISTS (SELECT 1 FROM assignments WHERE activation_id=?)`, activationID, activationID).Scan(&count)
	return count == 1, err
}

// ReconcileUncertainDispatch releases an uncertain dispatch only after its
// old assignment has been explicitly marked terminal by the coordinator.
func (s *Store) ReconcileUncertainDispatch(activationID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var assignmentState, dispatchState string
	err = tx.QueryRow(`SELECT state FROM assignments WHERE activation_id=?`, activationID).Scan(&assignmentState)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == sql.ErrNoRows {
		assignmentState = ""
	}
	if assignmentState != "" && assignmentState != "completed" && assignmentState != "failed" && assignmentState != "cancelled" {
		return fmt.Errorf("muxlio: activation %q assignment is not reconciled", activationID)
	}
	if err = tx.QueryRow(`SELECT state FROM dispatches WHERE activation_id=?`, activationID).Scan(&dispatchState); err != nil {
		return err
	}
	if dispatchState == "uncertain" {
		if assignmentState == "" {
			return fmt.Errorf("muxlio: activation %q has no reconciled assignment", activationID)
		}
		if _, err = tx.Exec(`DELETE FROM exclusive_resources WHERE activation_id=?`, activationID); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE dispatches SET state='pending' WHERE activation_id=? AND state='uncertain'`, activationID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ClaimDispatch reserves all budgets and claims one request atomically.
// Least-recently-served runs rotate fairly; FIFO is used within each run.
// Only one request is prefetched per worker poll.
func (s *Store) ClaimDispatch(workerID string, lease time.Time) (*state.Dispatch, int64, error) {
	return s.ClaimDispatchForEpoch(workerID, lease, 0)
}

func (s *Store) ClaimDispatchForEpoch(workerID string, lease time.Time, epoch int64) (*state.Dispatch, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	if err = checkCoordinatorEpoch(tx, epoch); err != nil {
		return nil, 0, err
	}
	if err = recoverLostDispatches(tx); err != nil {
		return nil, 0, err
	}
	// Capacity is a concurrency limit, not merely an advertised hint. Count
	// only unexpired active leases so a worker can recover after a coordinator
	// outage without waiting for an explicit cleanup pass.
	var capacity int
	var capRaw, labelsRaw string
	if err = tx.QueryRow(`SELECT capacity,capabilities_json,labels_json FROM workers WHERE id=? AND draining=0`, workerID).Scan(&capacity, &capRaw, &labelsRaw); err != nil {
		if err == sql.ErrNoRows {
			return nil, 0, tx.Commit()
		}
		return nil, 0, err
	}
	caps, err := decodeWorkerCapabilities(capRaw)
	if err != nil {
		return nil, 0, err
	}
	var workerLabels map[string]string
	if err := json.Unmarshal([]byte(labelsRaw), &workerLabels); err != nil {
		return nil, 0, err
	}
	cfg, err := readAdmissionConfig(tx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := tx.Query(`SELECT d.assignment_id,d.run_id,d.activation_id,d.attempt_id,d.executor,d.request_json FROM dispatches d LEFT JOIN admission_turns t ON t.run_id=d.run_id WHERE d.state='pending' AND d.run_id IN (SELECT id FROM runs WHERE state IN ('created','running','waiting')) ORDER BY COALESCE(t.turn,0),d.rowid`)
	if err != nil {
		return nil, 0, err
	}
	var selected *state.Dispatch
	var demand state.AdmissionDemand
	for rows.Next() {
		var d state.Dispatch
		var raw string
		if err = rows.Scan(&d.AssignmentID, &d.RunID, &d.ActivationID, &d.AttemptID, &d.Executor, &raw); err != nil {
			rows.Close()
			return nil, 0, err
		}
		var envelope workers.ExecuteRequest
		if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
			rows.Close()
			return nil, 0, err
		}
		now := time.Now()
		if (envelope.QueueDeadline != nil && !now.Before(*envelope.QueueDeadline)) || (envelope.Deadline != nil && !now.Before(*envelope.Deadline)) {
			continue
		}
		if !workers.MatchWorkerDispatch(workerID, d.Executor, []byte(raw), caps, workerLabels) {
			continue
		}
		demand = admissionDemand(cfg, d.Executor)
		d.Request = []byte(raw)
		reason, checkErr := admissionReason(tx, cfg, d, demand, workerID, capacity, time.Now())
		if checkErr != nil {
			rows.Close()
			return nil, 0, checkErr
		}
		if _, err = tx.Exec(`INSERT INTO admission_queue(activation_id,reason) VALUES(?,?) ON CONFLICT(activation_id) DO UPDATE SET reason=excluded.reason`, d.ActivationID, reason); err != nil {
			rows.Close()
			return nil, 0, err
		}
		if reason != "" {
			continue
		}
		if envelope.TimeoutSeconds != nil && *envelope.TimeoutSeconds > 0 {
			deadline := time.Now().Add(time.Duration(*envelope.TimeoutSeconds * float64(time.Second)))
			envelope.ExecutionDeadline = &deadline
			encoded, err := json.Marshal(envelope)
			if err != nil {
				rows.Close()
				return nil, 0, err
			}
			raw = string(encoded)
			if _, err := tx.Exec(`UPDATE dispatches SET request_json=? WHERE attempt_id=?`, raw, d.AttemptID); err != nil {
				rows.Close()
				return nil, 0, err
			}
		}
		d.Request = []byte(raw)
		selected = &d
		break
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, 0, err
	}
	if selected == nil {
		return nil, 0, tx.Commit()
	}
	if err = reserveAdmission(tx, cfg, *selected, demand, time.Now()); err != nil {
		return nil, 0, err
	}
	var generation int64
	err = tx.QueryRow(`SELECT generation FROM assignments WHERE activation_id=?`, selected.ActivationID).Scan(&generation)
	if err != nil && err != sql.ErrNoRows {
		return nil, 0, err
	}
	generation++
	if _, err = tx.Exec(`INSERT INTO assignments(activation_id,run_id,worker_id,generation,lease_until,state,assignment_id,attempt_id) VALUES(?,?,?,?,?,'active',?,?) ON CONFLICT(activation_id) DO UPDATE SET run_id=excluded.run_id,worker_id=excluded.worker_id,generation=excluded.generation,lease_until=excluded.lease_until,state='active',assignment_id=excluded.assignment_id,attempt_id=excluded.attempt_id`, selected.ActivationID, selected.RunID, workerID, generation, rfc3339(lease), selected.AssignmentID, selected.AttemptID); err != nil {
		return nil, 0, err
	}
	if _, err = tx.Exec(`UPDATE assignments SET coordinator_epoch=? WHERE activation_id=?`, epoch, selected.ActivationID); err != nil {
		return nil, 0, err
	}
	if err := registerWorkerTiming(tx, state.Assignment{AttemptID: selected.AttemptID, AssignmentID: selected.AssignmentID, RunID: selected.RunID, ActivationID: selected.ActivationID, WorkerID: workerID}, generation); err != nil {
		return nil, 0, err
	}
	if _, err = tx.Exec(`UPDATE dispatches SET state='assigned' WHERE assignment_id=?`, selected.AssignmentID); err != nil {
		return nil, 0, err
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, err
	}
	coordinatorlog.WorkerAssigned(selected.RunID, selected.ActivationID, workerID, selected.Executor, selected.AttemptID)
	return selected, generation, nil
}

// AppendLogChunk persists one bounded log chunk; sequence numbers are per stream.
func (s *Store) AppendLogChunk(c state.LogChunk) error {
	if len(c.Data) > 64*1024 {
		return fmt.Errorf("muxlio: log chunk exceeds 64 KiB")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := persistProgress(tx, c); err != nil {
		return err
	}
	return tx.Commit()
}

// ScheduleTimer durably records a timer.
func (s *Store) ScheduleTimer(id, runID, activationID string, due time.Time, payload any) error {
	raw, err := marshalJSON(payload)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.Exec(`INSERT INTO timers(id,run_id,activation_id,due_at,state,payload_json) VALUES(?,?,?,?,'pending',?) ON CONFLICT(id) DO NOTHING`, id, runID, activationID, rfc3339(due), raw)
	return err
}

// GetTimer reads a durable deadline without consuming its wakeup.
func (s *Store) GetTimer(id string) (*state.Timer, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var timer state.Timer
	var due string
	var payload []byte
	err := s.db.QueryRow(`SELECT id,run_id,activation_id,due_at,state,payload_json FROM timers WHERE id=?`, id).Scan(&timer.ID, &timer.RunID, &timer.ActivationID, &due, &timer.State, &payload)
	if err != nil {
		return nil, err
	}
	timer.DueAt, err = time.Parse(time.RFC3339Nano, due)
	if err != nil {
		return nil, err
	}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &timer.Payload); err != nil {
			return nil, err
		}
	}
	return &timer, nil
}

// DueTimers returns pending timers due at now and marks them fired transactionally.
func (s *Store) DueTimers(now time.Time) ([]state.Timer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT id, run_id, activation_id, due_at, payload_json FROM timers WHERE state='pending' AND due_at<=? ORDER BY due_at`, rfc3339(now))
	if err != nil {
		return nil, err
	}
	var timers []state.Timer
	for rows.Next() {
		var t state.Timer
		var due, payload sql.NullString
		if err = rows.Scan(&t.ID, &t.RunID, &t.ActivationID, &due, &payload); err != nil {
			rows.Close()
			return nil, err
		}
		t.DueAt, _ = time.Parse(time.RFC3339Nano, due.String)
		t.State = "fired"
		if payload.Valid && payload.String != "" {
			t.Payload = map[string]any{}
			_ = json.Unmarshal([]byte(payload.String), &t.Payload)
		}
		timers = append(timers, t)
	}
	rows.Close()
	for _, t := range timers {
		if _, err = tx.Exec(`UPDATE timers SET state='fired' WHERE id=?`, t.ID); err != nil {
			return nil, err
		}
	}
	return timers, tx.Commit()
}

// CancelTimer marks a timer as cancelled.
func (s *Store) CancelTimer(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE timers SET state='cancelled' WHERE id=? AND state='pending'`, id)
	return err
}

// SendSignal appends a durable signal for a run.
// If an idempotencyKey is supplied, a second call with the same key for the
// same run+name is a no-op (the first delivery wins).
func (s *Store) SendSignal(runID, name string, payload any, idempotencyKey ...string) error {
	raw, err := marshalJSON(payload)
	if err != nil {
		return err
	}
	ikey := ""
	if len(idempotencyKey) > 0 {
		ikey = idempotencyKey[0]
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Deduplicate by idempotency key when provided.
	if ikey != "" {
		var existing int
		_ = s.db.QueryRow(`SELECT COUNT(*) FROM signals WHERE run_id=? AND name=? AND idempotency_key=?`, runID, name, ikey).Scan(&existing)
		if existing > 0 {
			return nil
		}
	}
	_, err = s.db.Exec(`INSERT INTO signals(run_id,name,seq,payload_json,timestamp,idempotency_key) SELECT ?,?,?,?,?,?`, runID, name, nextSignalSeq(s.db, runID, name), raw, rfc3339(time.Now()), ikey)
	return err
}
func nextSignalSeq(db *sql.DB, runID, name string) int {
	var n int
	_ = db.QueryRow(`SELECT COALESCE(MAX(seq),0)+1 FROM signals WHERE run_id=? AND name=?`, runID, name).Scan(&n)
	return n
}

// ConsumeSignals returns all signals for a run+name pair.
func (s *Store) ConsumeSignals(runID, name string) ([]state.Signal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT run_id, name, seq, payload_json, timestamp, idempotency_key FROM signals WHERE run_id=? AND name=? ORDER BY seq`, runID, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sigs []state.Signal
	for rows.Next() {
		var sig state.Signal
		var ts, payload, ikey sql.NullString
		if err := rows.Scan(&sig.RunID, &sig.Name, &sig.Seq, &payload, &ts, &ikey); err != nil {
			return nil, err
		}
		sig.Timestamp, _ = time.Parse(time.RFC3339Nano, ts.String)
		if payload.Valid && payload.String != "" {
			sig.Payload = map[string]any{}
			_ = json.Unmarshal([]byte(payload.String), &sig.Payload)
		}
		sig.IdempotencyKey = ikey.String
		sigs = append(sigs, sig)
	}
	return sigs, rows.Err()
}

// --- input requests ---

func scanInputRequest(row interface {
	Scan(dest ...any) error
}) (state.InputRequest, error) {
	var req state.InputRequest
	var createdAt string
	var schema, response, permissions, expiresAt, fulfilledAt sql.NullString
	if err := row.Scan(&req.RunID, &req.RequestID, &req.ActivationID, &req.Name, &schema, &expiresAt, &permissions, &req.State, &response, &req.ResponderID, &createdAt, &fulfilledAt, &req.IdempotencyKey); err != nil {
		return req, err
	}
	var err error
	req.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return req, err
	}
	if schema.Valid {
		req.Schema = json.RawMessage(schema.String)
	}
	if expiresAt.Valid {
		if t, err := time.Parse(time.RFC3339Nano, expiresAt.String); err == nil {
			req.ExpiresAt = &t
		}
	}
	if permissions.Valid && permissions.String != "" {
		_ = json.Unmarshal([]byte(permissions.String), &req.Permissions)
	}
	if response.Valid && response.String != "" {
		decoder := json.NewDecoder(strings.NewReader(response.String))
		decoder.UseNumber()
		if err := decoder.Decode(&req.Response); err != nil {
			return req, err
		}
	}
	if fulfilledAt.Valid {
		if t, err := time.Parse(time.RFC3339Nano, fulfilledAt.String); err == nil {
			req.FulfilledAt = &t
		}
	}
	return req, nil
}

// CreateInputRequest is idempotent: recreating a request for an existing run
// and request ID is a no-op (INSERT OR IGNORE), so run retries preserve an
// already-accepted response.
func (s *Store) CreateInputRequest(req *state.InputRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if req.State == "" {
		req.State = state.InputRequestPending
	}
	if req.CreatedAt.IsZero() {
		req.CreatedAt = time.Now()
	}
	permJSON, _ := json.Marshal(req.Permissions)
	var expiresAt any
	if req.ExpiresAt != nil {
		expiresAt = rfc3339(*req.ExpiresAt)
	}
	_, err := s.db.Exec(`INSERT OR IGNORE INTO input_requests(run_id,request_id,activation_id,name,schema_json,expires_at,permissions_json,state,response_json,responder_id,created_at,fulfilled_at,idempotency_key) VALUES(?,?,?,?,?,?,?,?,?,?,?,NULL,?)`,
		req.RunID, req.RequestID, req.ActivationID, req.Name, nullableJSON(req.Schema), expiresAt, string(permJSON), req.State, nullableJSON(nil), req.ResponderID, rfc3339(req.CreatedAt), req.IdempotencyKey)
	return err
}

func nullableJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}

func (s *Store) GetInputRequest(runID, requestID string) (*state.InputRequest, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	row := s.db.QueryRow(`SELECT run_id, request_id, activation_id, name, schema_json, expires_at, permissions_json, state, response_json, responder_id, created_at, fulfilled_at, idempotency_key FROM input_requests WHERE run_id=? AND request_id=?`, runID, requestID)
	req, err := scanInputRequest(row)
	if err != nil {
		return nil, fmt.Errorf("muxlio: input request %q not found for run %q: %w", requestID, runID, err)
	}
	return &req, nil
}

func (s *Store) ListInputRequests(runID string) ([]state.InputRequest, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(`SELECT run_id, request_id, activation_id, name, schema_json, expires_at, permissions_json, state, response_json, responder_id, created_at, fulfilled_at, idempotency_key FROM input_requests WHERE run_id=? ORDER BY created_at, request_id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []state.InputRequest
	for rows.Next() {
		req, err := scanInputRequest(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, req)
	}
	return result, rows.Err()
}

// SubmitInputResponse delivers a response at most once. The first submission
// with a given idempotency key fulfills the request; a second call with the
// same key returns the already-accepted request (accepted=false). A request
// already fulfilled with a different key, or expired/cancelled, is rejected.
func (s *Store) SubmitInputResponse(runID, requestID string, response any, responderID, idempotencyKey string) (*state.InputRequest, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.db.QueryRow(`SELECT run_id, request_id, activation_id, name, schema_json, expires_at, permissions_json, state, response_json, responder_id, created_at, fulfilled_at, idempotency_key FROM input_requests WHERE run_id=? AND request_id=?`, runID, requestID)
	req, err := scanInputRequest(row)
	if err != nil {
		return nil, false, fmt.Errorf("muxlio: input request %q not found for run %q: %w", requestID, runID, err)
	}
	if req.State == state.InputRequestFulfilled {
		if idempotencyKey != "" && req.IdempotencyKey == idempotencyKey {
			return &req, false, nil // idempotent replay
		}
		return nil, false, state.ErrInputRequestNotAccepting
	}
	if req.State == state.InputRequestPending && req.ExpiresAt != nil && !req.ExpiresAt.After(time.Now()) {
		if _, err := s.db.Exec(`UPDATE input_requests SET state='expired' WHERE run_id=? AND request_id=? AND state='pending'`, runID, requestID); err != nil {
			return nil, false, err
		}
		req.State = state.InputRequestExpired
	}
	if req.State != state.InputRequestPending {
		return nil, false, state.ErrInputRequestNotAccepting
	}
	respJSON, err := json.Marshal(response)
	if err != nil {
		return nil, false, err
	}
	now := time.Now()
	res, err := s.db.Exec(`UPDATE input_requests SET state='fulfilled', response_json=?, responder_id=?, fulfilled_at=?, idempotency_key=? WHERE run_id=? AND request_id=? AND state='pending' AND (expires_at IS NULL OR julianday(expires_at)>julianday(?))`,
		string(respJSON), responderID, rfc3339(now), idempotencyKey, runID, requestID, rfc3339(now))
	if err != nil {
		return nil, false, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return nil, false, err
	} else if n != 1 {
		current, err := scanInputRequest(s.db.QueryRow(`SELECT run_id, request_id, activation_id, name, schema_json, expires_at, permissions_json, state, response_json, responder_id, created_at, fulfilled_at, idempotency_key FROM input_requests WHERE run_id=? AND request_id=?`, runID, requestID))
		if err != nil {
			return nil, false, err
		}
		if current.State == state.InputRequestFulfilled && idempotencyKey != "" && current.IdempotencyKey == idempotencyKey {
			return &current, false, nil
		}
		return nil, false, state.ErrInputRequestNotAccepting
	}
	req.State = state.InputRequestFulfilled
	req.Response = response
	req.ResponderID = responderID
	req.IdempotencyKey = idempotencyKey
	req.FulfilledAt = &now
	return &req, true, nil
}

func (s *Store) ExpireInputRequests(now time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`UPDATE input_requests SET state='expired' WHERE state='pending' AND expires_at IS NOT NULL AND julianday(expires_at) <= julianday(?)`, rfc3339(now))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
func (s *Store) GetLogChunks(runID, activationID string) ([]state.LogChunk, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	query := `SELECT run_id, activation_id, stream, seq, data, timestamp FROM log_chunks WHERE run_id=?`
	args := []any{runID}
	if activationID != "" {
		query += ` AND activation_id=?`
		args = append(args, activationID)
	}
	query += ` ORDER BY activation_id, stream, seq`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var chunks []state.LogChunk
	for rows.Next() {
		var c state.LogChunk
		var ts string
		if err := rows.Scan(&c.RunID, &c.ActivationID, &c.Stream, &c.Seq, &c.Data, &ts); err != nil {
			return nil, err
		}
		c.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
		chunks = append(chunks, c)
	}
	return chunks, rows.Err()
}

func (s *Store) ListAssignments(runID string) ([]state.Assignment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT run_id, activation_id, worker_id, generation, lease_until, state, assignment_id, attempt_id, coordinator_epoch FROM assignments WHERE run_id=? ORDER BY activation_id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]state.Assignment, 0)
	for rows.Next() {
		var a state.Assignment
		var lease string
		if err := rows.Scan(&a.RunID, &a.ActivationID, &a.WorkerID, &a.Generation, &lease, &a.State, &a.AssignmentID, &a.AttemptID, &a.CoordinatorEpoch); err != nil {
			return nil, err
		}
		a.LeaseUntil, _ = time.Parse(time.RFC3339Nano, lease)
		result = append(result, a)
	}
	return result, rows.Err()
}

func (s *Store) CancelRunWork(runID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE assignments SET state='cancelled', generation=generation+1 WHERE run_id=? AND state IN ('active','uncertain')`, runID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE dispatches SET state='cancelled' WHERE run_id=? AND state IN ('pending','assigned','uncertain')`, runID); err != nil {
		return err
	}
	rows, err := tx.Query(`SELECT id FROM activations WHERE run_id=? AND state NOT IN ('succeeded','failed','cancelled')`, runID)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, id := range ids {
		if _, err := tx.Exec(`UPDATE activations SET state='cancelled' WHERE id=?`, id); err != nil {
			return err
		}
		if err := appendEvent(tx, runID, state.EventActivationCancelled, id, map[string]any{"termination": "unconfirmed", "externalEffects": "completion unknown"}); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE timers SET state='cancelled' WHERE run_id=? AND state='pending'`, runID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE input_requests SET state='cancelled' WHERE run_id=? AND state='pending'`, runID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RecordExecutionAttempt(a state.ExecutionAttempt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	usageJSON, err := marshalUsage(a.Usage)
	if err != nil {
		return err
	}
	var measurementsJSON any
	if a.Measurements != nil {
		raw, err := json.Marshal(a.Measurements)
		if err != nil {
			return err
		}
		measurementsJSON = string(raw)
	}
	var resultJSON any
	if a.Result != nil {
		raw, marshalErr := json.Marshal(a.Result)
		if marshalErr != nil {
			return marshalErr
		}
		resultJSON = string(raw)
	}
	var operationRefJSON any
	if a.OperationRef != nil {
		raw, marshalErr := json.Marshal(a.OperationRef)
		if marshalErr != nil {
			return marshalErr
		}
		operationRefJSON = string(raw)
	}
	_, err = tx.Exec(`INSERT INTO execution_attempts(id,run_id,activation_id,retry_number,executor,state,started_at,finished_at,failure,usage_json,result_json,execution_duration_ms,effect,idempotency_key,operation_ref_json,await_reconciliation_reason,measurements_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,COALESCE((SELECT SUM(duration_ms) FROM worker_execution_timings WHERE attempt_id=?),?),?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET measurements_json=excluded.measurements_json,state=excluded.state,finished_at=excluded.finished_at,failure=excluded.failure,usage_json=excluded.usage_json,result_json=excluded.result_json,execution_duration_ms=COALESCE((SELECT SUM(duration_ms) FROM worker_execution_timings WHERE attempt_id=excluded.id),excluded.execution_duration_ms,execution_attempts.execution_duration_ms),effect=COALESCE(NULLIF(execution_attempts.effect,''),excluded.effect),idempotency_key=COALESCE(NULLIF(execution_attempts.idempotency_key,''),excluded.idempotency_key),operation_ref_json=COALESCE(excluded.operation_ref_json,execution_attempts.operation_ref_json),await_reconciliation_reason=excluded.await_reconciliation_reason`, a.ID, a.RunID, a.ActivationID, a.RetryNumber, a.Executor, a.State, rfc3339(a.StartedAt), rfc3339(a.FinishedAt), a.Failure, usageJSON, resultJSON, a.ID, a.ExecutionDurationMS, a.Effect, a.IdempotencyKey, operationRefJSON, a.AwaitReconciliationReason, measurementsJSON)
	if err != nil {
		return err
	}
	var recordedDuration sql.NullInt64
	if err := tx.QueryRow(`SELECT execution_duration_ms FROM execution_attempts WHERE id=?`, a.ID).Scan(&recordedDuration); err != nil {
		return err
	}
	if recordedDuration.Valid {
		a.ExecutionDurationMS = &recordedDuration.Int64
	}
	payload := map[string]any{"attemptId": a.ID, "retryNumber": a.RetryNumber, "executor": a.Executor, "state": a.State, "startedAt": a.StartedAt, "finishedAt": a.FinishedAt, "failure": a.Failure}
	if a.Result != nil {
		payload["result"] = a.Result
	}
	if a.Measurements != nil {
		payload["measurements"] = a.Measurements
	}
	if a.Usage != nil {
		payload["usage"] = a.Usage
	}
	if a.ExecutionDurationMS != nil {
		payload["executionDurationMs"] = *a.ExecutionDurationMS
	}
	if a.Effect != "" {
		payload["effect"] = a.Effect
	}
	if a.IdempotencyKey != "" {
		payload["idempotencyKey"] = a.IdempotencyKey
	}
	if a.OperationRef != nil {
		payload["operationRef"] = a.OperationRef
	}
	if a.AwaitReconciliationReason != "" {
		payload["awaitReconciliationReason"] = a.AwaitReconciliationReason
	}
	if err := appendEvent(tx, a.RunID, state.EventExecutionAttempt, a.ActivationID, payload); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	switch a.State {
	case "running":
		coordinatorlog.AttemptStarted(a.RunID, a.ActivationID, a.Executor, a.RetryNumber)
	case "succeeded", "failed", "cancelled":
		coordinatorlog.AttemptFinished(a.RunID, a.ActivationID, a.Executor, a.RetryNumber, a.State, a.Failure)
	}
	return nil
}

func (s *Store) ListExecutionAttempts(runID, activationID string) ([]state.ExecutionAttempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	query := `SELECT id,run_id,activation_id,retry_number,executor,state,started_at,finished_at,failure,usage_json,result_json,execution_duration_ms,effect,idempotency_key,operation_ref_json,await_reconciliation_reason,measurements_json FROM execution_attempts WHERE run_id=?`
	args := []any{runID}
	if activationID != "" {
		query += ` AND activation_id=?`
		args = append(args, activationID)
	}
	query += ` ORDER BY activation_id,retry_number,id`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []state.ExecutionAttempt
	for rows.Next() {
		var a state.ExecutionAttempt
		var started, finished string
		var usageJSON, resultJSON, measurementsJSON sql.NullString
		var execDuration sql.NullInt64
		var operationRefJSON, effect, idempotencyKey, awaitReconciliation sql.NullString
		if err := rows.Scan(&a.ID, &a.RunID, &a.ActivationID, &a.RetryNumber, &a.Executor, &a.State, &started, &finished, &a.Failure, &usageJSON, &resultJSON, &execDuration, &effect, &idempotencyKey, &operationRefJSON, &awaitReconciliation, &measurementsJSON); err != nil {
			return nil, err
		}
		a.StartedAt, err = time.Parse(time.RFC3339Nano, started)
		if err != nil {
			return nil, err
		}
		a.FinishedAt, err = time.Parse(time.RFC3339Nano, finished)
		if err != nil {
			return nil, err
		}
		if resultJSON.Valid {
			if err := json.Unmarshal([]byte(resultJSON.String), &a.Result); err != nil {
				return nil, err
			}
		}
		if measurementsJSON.Valid {
			if err := json.Unmarshal([]byte(measurementsJSON.String), &a.Measurements); err != nil {
				return nil, err
			}
		}
		a.Usage, err = unmarshalUsage(usageJSON)
		if execDuration.Valid {
			value := execDuration.Int64
			a.ExecutionDurationMS = &value
		}
		if effect.Valid {
			a.Effect = effect.String
		}
		if idempotencyKey.Valid {
			a.IdempotencyKey = idempotencyKey.String
		}
		if operationRefJSON.Valid && operationRefJSON.String != "" {
			var ref contract.RemoteTaskReference
			if err := json.Unmarshal([]byte(operationRefJSON.String), &ref); err != nil {
				return nil, err
			}
			a.OperationRef = &ref
		}
		if awaitReconciliation.Valid {
			a.AwaitReconciliationReason = awaitReconciliation.String
		}
		if err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	return result, rows.Err()
}

func (s *Store) CreateRun(run *state.Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run.CreatedAt.IsZero() {
		run.CreatedAt = time.Now()
	}
	run.UpdatedAt = run.CreatedAt
	run.State = "created"

	inputJSON, err := marshalJSON(run.Input)
	if err != nil {
		return err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.Exec(
		`INSERT INTO runs (id, workflow_name, state, input_json, error, created_at, updated_at, plan_json, plan_digest)
		 VALUES (?, ?, ?, ?, '', ?, ?, ?, ?)`,
		run.ID, run.WorkflowName, run.State, inputJSON, rfc3339(run.CreatedAt), rfc3339(run.UpdatedAt), []byte(run.PlanJSON), run.PlanDigest,
	)
	if err != nil {
		return fmt.Errorf("muxlio: inserting run: %w", err)
	}
	if err := appendEvent(tx, run.ID, state.EventRunCreated, run.ID, nil); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetRun(id string) (*state.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.db.QueryRow(
		`SELECT id, workflow_name, state, input_json, output_json, error, created_at, updated_at, plan_json, plan_digest
		 FROM runs WHERE id = ?`, id)
	return scanRun(row)
}

func (s *Store) SetRunState(id, st string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`UPDATE runs SET state = ?, updated_at = ? WHERE id = ?`, st, rfc3339(time.Now()), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("muxlio: run %q not found", id)
	}
	if eventType := state.EventTypeForRunState(st); eventType != "" {
		if err := appendEvent(tx, id, eventType, id, nil); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) TransitionRunState(id string, expected []string, next string) (bool, error) {
	if len(expected) == 0 {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var current string
	if err := tx.QueryRow(`SELECT state FROM runs WHERE id=?`, id).Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, fmt.Errorf("muxlio: run %q not found", id)
		}
		return false, err
	}
	allowed := false
	for _, state := range expected {
		if current == state {
			allowed = true
			break
		}
	}
	if !allowed {
		return false, nil
	}
	if _, err := tx.Exec(`UPDATE runs SET state=?, updated_at=? WHERE id=?`, next, rfc3339(time.Now()), id); err != nil {
		return false, err
	}
	if eventType := state.EventTypeForRunState(next); eventType != "" {
		if err := appendEvent(tx, id, eventType, id, nil); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	coordinatorlog.RunStateChanged(id, current, next)
	return true, nil
}

// RetryRun atomically requeues incomplete activations and restarts a terminal run.
func (s *Store) RetryRun(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current string
	if err := tx.QueryRow(`SELECT state FROM runs WHERE id=?`, id).Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("muxlio: run %q not found", id)
		}
		return err
	}
	if current != "failed" && current != "cancelled" {
		return fmt.Errorf("muxlio: run %q cannot be retried from state %q", id, current)
	}
	var held int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM execution_attempts WHERE run_id=? AND await_reconciliation_reason<>''`, id).Scan(&held); err != nil {
		return err
	}
	if held > 0 {
		return fmt.Errorf("run has attempts awaiting reconciliation; resolve their external outcomes before retrying")
	}
	rows, err := tx.Query(`SELECT id, node_id, scope_path_json FROM activations WHERE run_id=? AND state!='succeeded'`, id)
	if err != nil {
		return err
	}
	type queuedActivation struct {
		id, node string
		scope    []string
	}
	var queued []queuedActivation
	for rows.Next() {
		var a queuedActivation
		var scopeJSON string
		if err := rows.Scan(&a.id, &a.node, &scopeJSON); err != nil {
			rows.Close()
			return err
		}
		if err := json.Unmarshal([]byte(scopeJSON), &a.scope); err != nil {
			rows.Close()
			return err
		}
		queued = append(queued, a)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(queued) > 0 {
		activationIDs := make([]string, len(queued))
		for i, a := range queued {
			activationIDs[i] = a.id
		}
		if err := retryClearWork(tx, id, activationIDs); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE activations SET state='queued', error='', attempt_base=MAX(attempt_base, COALESCE((SELECT MAX(retry_number) FROM execution_attempts WHERE activation_id=activations.id),0)) WHERE run_id=? AND state!='succeeded'`, id); err != nil {
		return err
	}
	for _, a := range queued {
		if err := appendEvent(tx, id, state.EventActivationQueued, a.id, map[string]any{"nodeId": a.node, "scopePath": a.scope}); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE runs SET state='running', error='', updated_at=? WHERE id=?`, rfc3339(time.Now()), id); err != nil {
		return err
	}
	if err := appendEvent(tx, id, state.EventRunRunning, id, nil); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	coordinatorlog.RunRetry(id)
	coordinatorlog.RunStateChanged(id, current, "running")
	return nil
}

func retryClearWork(tx *sql.Tx, runID string, activationIDs []string) error {
	for _, actID := range activationIDs {
		if _, err := tx.Exec(`DELETE FROM log_chunks WHERE run_id=? AND activation_id=?`, runID, actID); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM worker_results WHERE attempt_id IN (SELECT id FROM execution_attempts WHERE run_id=? AND activation_id=?)`, runID, actID); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM dispatches WHERE run_id=? AND activation_id=?`, runID, actID); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE assignments SET state='cancelled', generation=generation+1 WHERE run_id=? AND activation_id=? AND state='active'`, runID, actID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) SetRunOutput(id string, output any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := marshalJSON(output)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(`UPDATE runs SET output_json = ?, updated_at = ? WHERE id = ? AND state<>'cancelled'`, raw, rfc3339(time.Now()), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("muxlio: run %q not found", id)
	}
	return nil
}

func (s *Store) UpdateRunPlan(id string, planJSON json.RawMessage, digest string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var pinned []byte
	var pinnedDigest string
	if err := s.db.QueryRow(`SELECT plan_json, plan_digest FROM runs WHERE id=?`, id).Scan(&pinned, &pinnedDigest); err != nil {
		return err
	}
	if pinnedDigest != digest || !bytes.Equal(pinned, planJSON) {
		return fmt.Errorf("muxlio: run %q has an immutable plan; create a new run", id)
	}
	return nil
}

func (s *Store) SetRunError(id string, runErr error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current string
	if err := tx.QueryRow(`SELECT state FROM runs WHERE id=?`, id).Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("muxlio: run %q not found", id)
		}
		return err
	}
	if current == "cancelled" || current == "succeeded" {
		return nil
	}
	res, err := tx.Exec(
		`UPDATE runs SET error = ?, state = 'failed', updated_at = ? WHERE id = ?`,
		runErr.Error(), rfc3339(time.Now()), id,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("muxlio: run %q not found", id)
	}
	if err := appendEvent(tx, id, state.EventRunFailed, id, map[string]any{"error": runErr.Error()}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	coordinatorlog.RunStateChanged(id, current, "failed")
	coordinatorlog.RunFailed(id, runErr.Error())
	return nil
}

func (s *Store) ListRuns() ([]*state.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(
		`SELECT id, workflow_name, state, input_json, output_json, error, created_at, updated_at, plan_json, plan_digest
		 FROM runs ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []*state.Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

func (s *Store) CreateActivation(a *state.Activation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	scopeJSON, err := marshalJSON(a.ScopePath)
	if err != nil {
		return err
	}
	if a.ScopePath == nil {
		scopeJSON = []byte("[]")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var cancelled int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM runs WHERE id=? AND state='cancelled'`, a.RunID).Scan(&cancelled); err != nil {
		return err
	}
	if cancelled != 0 {
		return context.Canceled
	}

	_, err = tx.Exec(
		`INSERT INTO activations (id, run_id, scope_path_json, node_id, occurrence, state, attempt_base, inputs_json, error)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, '')`,
		a.ID, a.RunID, string(scopeJSON), a.NodeID, a.Occurrence, a.State, a.AttemptBase, []byte(a.Inputs),
	)
	if err != nil {
		return fmt.Errorf("muxlio: inserting activation: %w", err)
	}
	if err := appendEvent(tx, a.RunID, state.EventActivationQueued, a.ID, map[string]any{
		"nodeId":    a.NodeID,
		"scopePath": a.ScopePath,
	}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetActivation(id string) (*state.Activation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.db.QueryRow(
		`SELECT id, run_id, scope_path_json, node_id, occurrence, state, outputs_json, error, attempt_base, inputs_json
		 FROM activations WHERE id = ?`, id)
	return scanActivation(row)
}

func (s *Store) ListActivations(runID string) ([]state.Activation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT id, run_id, scope_path_json, node_id, occurrence, state, outputs_json, error, attempt_base, inputs_json FROM activations WHERE run_id=? ORDER BY rowid`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]state.Activation, 0)
	for rows.Next() {
		a, err := scanActivation(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	reasons, err := s.db.Query(`SELECT d.activation_id, COALESCE(NULLIF(q.reason,''),'Waiting for worker') FROM dispatches d LEFT JOIN admission_queue q ON q.activation_id=d.activation_id WHERE d.run_id=? AND d.state='pending'`, runID)
	if err != nil {
		return nil, err
	}
	defer reasons.Close()
	byID := make(map[string]string)
	for reasons.Next() {
		var id, reason string
		if err := reasons.Scan(&id, &reason); err != nil {
			return nil, err
		}
		byID[id] = reason
	}
	if err := reasons.Err(); err != nil {
		return nil, err
	}
	for i := range result {
		result[i].QueueReason = byID[result[i].ID]
	}
	return result, nil
}

func (s *Store) SetActivationState(id, st string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var runID string
	if err := tx.QueryRow(`SELECT run_id FROM activations WHERE id = ?`, id).Scan(&runID); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("muxlio: activation %q not found", id)
		}
		return err
	}
	var current string
	if err := tx.QueryRow(`SELECT state FROM activations WHERE id=?`, id).Scan(&current); err != nil {
		return err
	}
	if current == "cancelled" {
		return nil
	}
	if _, err := tx.Exec(`UPDATE activations SET state = ? WHERE id = ?`, st, id); err != nil {
		return err
	}
	if eventType := state.EventTypeForActivationState(st); eventType != "" {
		if err := appendEvent(tx, runID, eventType, id, nil); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	switch st {
	case "running":
		coordinatorlog.ActivationStarted(runID, id)
	case "succeeded", "failed", "cancelled":
		coordinatorlog.ActivationFinished(runID, id, st)
	}
	return nil
}

func (s *Store) SetActivationOutputs(id string, outputs map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := marshalJSON(outputs)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var cancelled int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM runs WHERE id=(SELECT run_id FROM activations WHERE id=?) AND state='cancelled'`, id).Scan(&cancelled); err != nil {
		return err
	}
	if cancelled != 0 {
		return context.Canceled
	}

	var runID string
	if err := tx.QueryRow(`SELECT run_id FROM activations WHERE id = ?`, id).Scan(&runID); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("muxlio: activation %q not found", id)
		}
		return err
	}
	var current string
	if err := tx.QueryRow(`SELECT state FROM activations WHERE id=?`, id).Scan(&current); err != nil {
		return err
	}
	if current == "cancelled" || current == "succeeded" || current == "failed" {
		return nil
	}
	if _, err := tx.Exec(`UPDATE activations SET outputs_json = ?, state = 'succeeded' WHERE id = ?`, raw, id); err != nil {
		return err
	}
	if err := appendEvent(tx, runID, state.EventActivationSucceeded, id, map[string]any{
		"ports": stateKeys(outputs),
	}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	coordinatorlog.ActivationFinished(runID, id, "succeeded")
	return nil
}

func (s *Store) SetActivationError(id string, actErr error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var runID string
	if err := tx.QueryRow(`SELECT run_id FROM activations WHERE id = ?`, id).Scan(&runID); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("muxlio: activation %q not found", id)
		}
		return err
	}
	var current string
	if err := tx.QueryRow(`SELECT state FROM activations WHERE id=?`, id).Scan(&current); err != nil {
		return err
	}
	if current == "cancelled" || current == "succeeded" || current == "failed" {
		return nil
	}
	if _, err := tx.Exec(`UPDATE activations SET error = ?, state = 'failed' WHERE id = ?`, actErr.Error(), id); err != nil {
		return err
	}
	if err := appendEvent(tx, runID, state.EventActivationFailed, id, map[string]any{"error": actErr.Error()}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	coordinatorlog.ActivationFinished(runID, id, "failed")
	return nil
}

func (s *Store) GetEvents(runID string, after int, limit int) ([]state.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := `SELECT run_id, seq, timestamp, type, subject_id, payload_json FROM events WHERE run_id = ? AND seq > ? ORDER BY seq`
	args := []any{runID, after}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []state.Event
	for rows.Next() {
		var e state.Event
		var ts string
		var payload sql.NullString
		if err := rows.Scan(&e.RunID, &e.Seq, &ts, &e.Type, &e.SubjectID, &payload); err != nil {
			return nil, err
		}
		e.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
		if payload.Valid && payload.String != "" {
			payloadMap := map[string]any{}
			dec := json.NewDecoder(strings.NewReader(payload.String))
			dec.UseNumber()
			if err := dec.Decode(&payloadMap); err != nil {
				return nil, err
			}
			e.Payload = payloadMap
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

func (s *Store) AppendEvent(runID, eventType, subjectID string, payload map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return errors.New("store closed")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := appendEvent(tx, runID, eventType, subjectID, payload); err != nil {
		return err
	}
	return tx.Commit()
}

func appendEvent(tx *sql.Tx, runID, eventType, subjectID string, payload map[string]any) error {
	var seq int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM events WHERE run_id = ?`, runID).Scan(&seq); err != nil {
		return err
	}
	seq++
	raw, err := marshalJSON(payload)
	if err != nil {
		return err
	}
	var payloadArg any
	if payload != nil {
		payloadArg = string(raw)
	}
	_, err = tx.Exec(
		`INSERT INTO events (run_id, seq, timestamp, type, subject_id, payload_json) VALUES (?, ?, ?, ?, ?, ?)`,
		runID, seq, rfc3339(time.Now()), eventType, subjectID, payloadArg,
	)
	return err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanRun(row rowScanner) (*state.Run, error) {
	var r state.Run
	var pinned []byte
	var input, output sql.NullString
	var created, updated string
	if err := row.Scan(&r.ID, &r.WorkflowName, &r.State, &input, &output, &r.Error, &created, &updated, &pinned, &r.PlanDigest); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("muxlio: run not found")
		}
		return nil, err
	}
	r.PlanJSON = pinned
	r.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	r.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	if input.Valid && input.String != "" {
		if err := json.Unmarshal([]byte(input.String), &r.Input); err != nil {
			return nil, err
		}
	}
	if output.Valid && output.String != "" {
		if err := json.Unmarshal([]byte(output.String), &r.Output); err != nil {
			return nil, err
		}
	}
	return &r, nil
}

func scanActivation(row rowScanner) (*state.Activation, error) {
	var a state.Activation
	var scope, outputs sql.NullString
	var inputs []byte
	if err := row.Scan(&a.ID, &a.RunID, &scope, &a.NodeID, &a.Occurrence, &a.State, &outputs, &a.Error, &a.AttemptBase, &inputs); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("muxlio: activation not found")
		}
		return nil, err
	}
	a.Inputs = inputs
	if scope.Valid && scope.String != "" {
		if err := json.Unmarshal([]byte(scope.String), &a.ScopePath); err != nil {
			return nil, err
		}
	}
	if outputs.Valid && outputs.String != "" {
		if err := json.Unmarshal([]byte(outputs.String), &a.Outputs); err != nil {
			return nil, err
		}
	}
	return &a, nil
}

func marshalJSON(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	return json.Marshal(v)
}

func rfc3339(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// marshalUsage serialises attempt token usage to a JSON column value. A nil
// pointer (provider did not report usage) is stored as NULL so that it can be
// distinguished from a reported zero on read-back.
func marshalUsage(u *state.TokenUsage) ([]byte, error) {
	if u == nil {
		return nil, nil
	}
	return json.Marshal(u)
}

// unmarshalUsage deserialises the usage_json column. A NULL or empty value
// yields a nil pointer (explicitly unavailable); a present JSON object yields
// a non-nil pointer even when all counts are zero (reported as zero).
func unmarshalUsage(v sql.NullString) (*state.TokenUsage, error) {
	if !v.Valid || v.String == "" {
		return nil, nil
	}
	var u state.TokenUsage
	if err := json.Unmarshal([]byte(v.String), &u); err != nil {
		return nil, fmt.Errorf("muxlio: parsing attempt usage: %w", err)
	}
	return &u, nil
}

func stateKeys(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

func (s *Store) WorkerResult(id string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw []byte
	err := s.db.QueryRow(`SELECT result_json FROM worker_results WHERE attempt_id=?`, id).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return raw, err
}

func (s *Store) LookupWorkerAssignment(workerID, assignmentID string) (*state.Assignment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var a state.Assignment
	var lease string
	err := s.db.QueryRow(`SELECT run_id, activation_id, worker_id, generation, lease_until, state, assignment_id, attempt_id, coordinator_epoch FROM assignments WHERE assignment_id=? AND worker_id=?`, assignmentID, workerID).Scan(&a.RunID, &a.ActivationID, &a.WorkerID, &a.Generation, &lease, &a.State, &a.AssignmentID, &a.AttemptID, &a.CoordinatorEpoch)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	a.LeaseUntil, _ = time.Parse(time.RFC3339Nano, lease)
	return &a, nil
}

func (s *Store) RecordWorkerDiagnostic(attemptID string, payload []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO worker_diagnostics(attempt_id,payload_json,recorded_at) VALUES(?,?,?)`, attemptID, payload, rfc3339(time.Now()))
	return err
}

func (s *Store) WorkerDiagnostic(attemptID string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw []byte
	err := s.db.QueryRow(`SELECT payload_json FROM worker_diagnostics WHERE attempt_id=? ORDER BY id LIMIT 1`, attemptID).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return raw, err
}

// AckWorkerCompletionWithoutResult finalizes a revoked or stale assignment without
// recording an activation result.
func (s *Store) AckWorkerCompletionWithoutResult(a state.Assignment, terminalState string, workerGeneration int64) (bool, error) {
	if terminalState != "succeeded" && terminalState != "failed" && terminalState != "cancelled" {
		return false, fmt.Errorf("invalid worker completion state %q", terminalState)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE assignments SET state=? WHERE activation_id=? AND run_id=? AND worker_id=? AND assignment_id=? AND attempt_id=? AND generation=? AND state='cancelled'`, terminalState, a.ActivationID, a.RunID, a.WorkerID, a.AssignmentID, a.AttemptID, workerGeneration+1)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		res, err = tx.Exec(`UPDATE assignments SET state=? WHERE activation_id=? AND run_id=? AND worker_id=? AND assignment_id=? AND attempt_id=? AND generation=? AND state='cancelled'`, terminalState, a.ActivationID, a.RunID, a.WorkerID, a.AssignmentID, a.AttemptID, workerGeneration)
		if err != nil {
			return false, err
		}
		n, err = res.RowsAffected()
		if err != nil {
			return false, err
		}
	}
	if n != 1 {
		return false, tx.Commit()
	}
	if _, err := tx.Exec(`DELETE FROM exclusive_resources WHERE attempt_id=?`, a.AttemptID); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) EnqueueOutbox(id, kind string, payload any) error {
	var payloadJSON string
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("muxlio: marshaling outbox payload: %w", err)
		}
		payloadJSON = string(raw)
	} else {
		payloadJSON = "{}"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO outbox(id, kind, payload_json, created_at) VALUES(?, ?, ?, ?)`,
		id, kind, payloadJSON, rfc3339(time.Now()))
	return err
}

// ListUnpublishedOutbox returns unpublished messages ordered by creation time.
func (s *Store) ListUnpublishedOutbox(limit int) ([]state.OutboxRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	// Stored timestamps are UTC RFC3339Nano with variable-width fractions. Strip
	// the trailing Z for ordering so a fractional prefix sorts before its longer
	// successor (e.g. .619Z before .619022Z). Break equal timestamps by insertion.
	query := `SELECT id, kind, payload_json, created_at, published_at FROM outbox WHERE published_at IS NULL ORDER BY rtrim(created_at, 'Z') ASC, rowid ASC`
	var rows *sql.Rows
	var err error
	if limit > 0 {
		query += ` LIMIT ?`
		rows, err = s.db.Query(query, limit)
	} else {
		rows, err = s.db.Query(query)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []state.OutboxRecord
	for rows.Next() {
		var r state.OutboxRecord
		var rawPayload, createdAtStr string
		var publishedAtStr sql.NullString
		if err := rows.Scan(&r.ID, &r.Kind, &rawPayload, &createdAtStr, &publishedAtStr); err != nil {
			return nil, err
		}
		if createdAt, err := time.Parse(time.RFC3339Nano, createdAtStr); err == nil {
			r.CreatedAt = createdAt
		}
		if publishedAtStr.Valid {
			if pub, err := time.Parse(time.RFC3339Nano, publishedAtStr.String); err == nil {
				r.PublishedAt = &pub
			}
		}
		if rawPayload != "" {
			var p any
			if err := json.Unmarshal([]byte(rawPayload), &p); err == nil {
				r.Payload = p
			}
		}
		records = append(records, r)
	}
	return records, rows.Err()
}

// MarkOutboxPublished records when an outbox message was published.
func (s *Store) MarkOutboxPublished(id string, publishedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`UPDATE outbox SET published_at=? WHERE id=?`, rfc3339(publishedAt), id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("muxlio: outbox record %q not found", id)
	}
	return nil
}

func (s *Store) SaveWorkflowVersion(digest string, planJSON []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var existing []byte
	err := s.db.QueryRow(`SELECT plan_json FROM workflow_versions WHERE digest=?`, digest).Scan(&existing)
	if err == nil {
		if !bytes.Equal(existing, planJSON) {
			return fmt.Errorf("muxlio: workflow version %q is immutable", digest)
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.Exec(`INSERT INTO workflow_versions (digest, plan_json, created_at) VALUES (?, ?, ?)
		ON CONFLICT(digest) DO NOTHING`, digest, planJSON, now)
	return err
}

func (s *Store) GetWorkflowVersion(digest string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var raw []byte
	err := s.db.QueryRow(`SELECT plan_json FROM workflow_versions WHERE digest = ?`, digest).Scan(&raw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("workflow version %q: %w", digest, errors.New("not found"))
		}
		return nil, err
	}
	return raw, nil
}

func (s *Store) ListWorkflowVersions() ([]state.WorkflowVersionSummary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(`SELECT digest, plan_json, created_at FROM workflow_versions ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []state.WorkflowVersionSummary
	for rows.Next() {
		var digest, createdAt string
		var raw []byte
		if err := rows.Scan(&digest, &raw, &createdAt); err != nil {
			return nil, err
		}
		var p struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(raw, &p)
		list = append(list, state.WorkflowVersionSummary{
			Digest:    digest,
			Name:      p.Name,
			CreatedAt: createdAt,
		})
	}
	return list, rows.Err()
}

// RecordWorkerExecutionDuration retains a measurement even if the interpreter has
// stopped waiting (cancellation or recovery). A receipt is unique per worker
// assignment generation, so completion replay cannot add it twice.
func (s *Store) RecordWorkerExecutionDuration(a state.Assignment, duration int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Existing assignments from before the timing migration may have no receipt.
	if _, err = tx.Exec(`INSERT INTO worker_execution_timings(attempt_id,assignment_id,generation,run_id,activation_id,worker_id)
 SELECT attempt_id,assignment_id,?,run_id,activation_id,worker_id FROM assignments WHERE run_id=? AND activation_id=? AND worker_id=? AND assignment_id=? AND attempt_id=? AND (generation=? OR (generation=? AND state IN ('uncertain','cancelled'))) ON CONFLICT DO NOTHING`, a.Generation, a.RunID, a.ActivationID, a.WorkerID, a.AssignmentID, a.AttemptID, a.Generation, a.Generation+1); err != nil {
		return err
	}
	res, err := tx.Exec(`UPDATE worker_execution_timings SET duration_ms=? WHERE attempt_id=? AND assignment_id=? AND generation=? AND run_id=? AND activation_id=? AND worker_id=? AND duration_ms IS NULL`, duration, a.AttemptID, a.AssignmentID, a.Generation, a.RunID, a.ActivationID, a.WorkerID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	// An unknown identity or a replay must not mutate an attempt, including
	// clearing a measurement recorded by a local worker or a previous version.
	if n == 0 {
		return nil
	}
	var retryNumber int
	var executor, attemptState, started, finished, failure string
	var total int64
	err = tx.QueryRow(`UPDATE execution_attempts SET execution_duration_ms=(SELECT SUM(duration_ms) FROM worker_execution_timings WHERE attempt_id=?) WHERE id=? AND run_id=? AND activation_id=? RETURNING retry_number,executor,state,started_at,finished_at,failure,execution_duration_ms`, a.AttemptID, a.AttemptID, a.RunID, a.ActivationID).Scan(&retryNumber, &executor, &attemptState, &started, &finished, &failure, &total)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil {
		payload := map[string]any{"attemptId": a.AttemptID, "retryNumber": retryNumber, "executor": executor, "state": attemptState, "startedAt": started, "finishedAt": finished, "failure": failure, "executionDurationMs": total}
		if err := appendEvent(tx, a.RunID, state.EventExecutionAttempt, a.ActivationID, payload); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Keep assignment identity after a retry replaces the current assignment, so a
// late worker can still report the time it actually spent on the old attempt.
func registerWorkerTiming(tx *sql.Tx, a state.Assignment, generation int64) error {
	if a.AttemptID == "" || a.AssignmentID == "" {
		return nil
	}
	_, err := tx.Exec(`INSERT INTO worker_execution_timings(attempt_id,assignment_id,generation,run_id,activation_id,worker_id) VALUES(?,?,?,?,?,?) ON CONFLICT DO NOTHING`, a.AttemptID, a.AssignmentID, generation, a.RunID, a.ActivationID, a.WorkerID)
	return err
}
