package sqlite

import (
	"database/sql"
	"encoding/json"
	"github.com/muxlio/muxlio-core/internal/state"
	"time"
)

var _ state.OperationsStore = (*Store)(nil)

func (s *Store) AutomationHistoryPage(kind, id string, offset, limit int) ([]json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var query string
	var args []any
	switch kind {
	case "schedules":
		query = `SELECT json_object('scheduleId',schedule_id,'dueAt',due_at,'through',through_at,'state',state,'runId',run_id) FROM schedule_fires WHERE schedule_id=? ORDER BY julianday(due_at) DESC,due_at DESC LIMIT ? OFFSET ?`
		args = []any{id, limit, offset}
	case "triggers":
		query = `SELECT json_object('triggerId',trigger_id,'idempotencyKey',idempotency_key,'runId',run_id,'state',state,'reason',reason,'timestamp',timestamp) FROM trigger_deliveries WHERE trigger_id=? ORDER BY id DESC LIMIT ? OFFSET ?`
		args = []any{id, limit, offset}
	case "subscriptions":
		query = `SELECT json_remove(record_json,'$.payload') FROM subscription_deliveries WHERE subscription_id=? ORDER BY julianday(json_extract(record_json,'$.createdAt')) DESC,id DESC LIMIT ? OFFSET ?`
		args = []any{id, limit, offset}
	case "deadletters":
		query = `SELECT json_remove(record_json,'$.payload') FROM subscription_deliveries WHERE state='deadletter' ORDER BY julianday(json_extract(record_json,'$.createdAt')) DESC,id DESC LIMIT ? OFFSET ?`
		args = []any{limit, offset}
	default:
		return nil, sql.ErrNoRows
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []json.RawMessage{}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		items = append(items, json.RawMessage(raw))
	}
	return items, rows.Err()
}

func (s *Store) ArtifactRevisionPage(runID string, before, limit int) ([]state.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT run_id,seq,type,subject_id,payload_json,timestamp FROM events WHERE run_id=? AND type='artifact.snapshot' AND (?=0 OR seq<?) ORDER BY seq DESC LIMIT ?`, runID, before, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []state.Event{}
	for rows.Next() {
		var e state.Event
		var payload, stamp string
		if err = rows.Scan(&e.RunID, &e.Seq, &e.Type, &e.SubjectID, &payload, &stamp); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(payload), &e.Payload); err != nil {
			return nil, err
		}
		e.Timestamp, _ = time.Parse(time.RFC3339Nano, stamp)
		items = append(items, e)
	}
	return items, rows.Err()
}

func (s *Store) SetAutomationEnabled(kind, id string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch kind {
	case "schedules":
		var found int
		if err := s.db.QueryRow(`SELECT 1 FROM schedules WHERE id=?`, id).Scan(&found); err != nil {
			return err
		}
		_, err := s.db.Exec(`INSERT INTO automation_switches(kind,id,enabled) VALUES('schedules',?,?) ON CONFLICT(kind,id) DO UPDATE SET enabled=excluded.enabled`, id, enabled)
		return err
	case "triggers", "subscriptions":
		// Table names come only from this fixed enum, never a user-provided identifier.
		result, err := s.db.Exec(`UPDATE `+kind+` SET definition_json=json_set(definition_json,'$.enabled',json(?)) WHERE id=?`, map[bool]string{true: "true", false: "false"}[enabled], id)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err == nil && n == 0 {
			return sql.ErrNoRows
		}
		return err
	default:
		return sql.ErrNoRows
	}
}

// These views read accounting and identity columns only, not workflow payloads.
const operationalFacts = `WITH facts AS (
 SELECT 'queue' kind,d.run_id,d.activation_id,d.attempt_id,'queued' state,coalesce(nullif(q.reason,''),'Waiting for a compatible worker') reason,'' stamp
 FROM dispatches d JOIN runs r ON r.id=d.run_id LEFT JOIN admission_queue q ON q.activation_id=d.activation_id
 WHERE d.state='pending' AND r.state NOT IN ('succeeded','failed','cancelled')
 UNION ALL SELECT 'attention',run_id,activation_id,id,state,coalesce(await_reconciliation_reason,''),started_at FROM execution_attempts WHERE await_reconciliation_reason<>''
 UNION ALL SELECT 'detached',run_id,activation_id,id,state,'Remote operation is running without a worker slot',started_at FROM execution_attempts WHERE state='detached'
 UNION ALL SELECT 'recovery',run_id,subject_id,'',type,coalesce(json_extract(payload_json,'$.reason'),''),timestamp FROM events WHERE type IN ('run.recovered','run.recovery_blocked','lease.expired','execution.resolved','execution.reconciled','assignment.expired','worker.lost')
 UNION ALL SELECT 'runs',id,'','',state,error,updated_at FROM runs WHERE state IN ('created','running','waiting','paused','failed')
 ) `

func (s *Store) OperationalPage(kind string, offset, limit int) ([]state.OperationalItem, map[string]int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	counts := map[string]int{}
	rows, err := s.db.Query(operationalFacts + `SELECT kind,count(*) FROM facts GROUP BY kind`)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var k string
		var n int
		if err = rows.Scan(&k, &n); err != nil {
			rows.Close()
			return nil, nil, err
		}
		counts[k] = n
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	rows, err = s.db.Query(operationalFacts+`SELECT kind,run_id,activation_id,attempt_id,state,reason,stamp FROM facts WHERE (?='' OR kind=?) ORDER BY stamp DESC,run_id,activation_id,attempt_id LIMIT ? OFFSET ?`, kind, kind, limit, offset)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	items := []state.OperationalItem{}
	for rows.Next() {
		var item state.OperationalItem
		if err = rows.Scan(&item.Kind, &item.RunID, &item.ActivationID, &item.AttemptID, &item.State, &item.Reason, &item.At); err != nil {
			return nil, nil, err
		}
		items = append(items, item)
	}
	return items, counts, rows.Err()
}
func (s *Store) ListRetentionPins() ([]state.RetentionPin, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT kind,id FROM retention_pins ORDER BY kind,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []state.RetentionPin{}
	for rows.Next() {
		var p state.RetentionPin
		if err = rows.Scan(&p.Kind, &p.ID); err != nil {
			return nil, err
		}
		items = append(items, p)
	}
	return items, rows.Err()
}
func (s *Store) SaveOperationalReport(r state.OperationalReport) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT OR IGNORE INTO operational_reports(id,kind,name,imported_at,document) VALUES(?,?,?,?,?)`, r.ID, r.Kind, r.Name, rfc3339(r.ImportedAt), []byte(r.Document))
	return err
}
func (s *Store) ListOperationalReports(offset, limit int) ([]state.OperationalReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT id,kind,name,imported_at FROM operational_reports ORDER BY imported_at DESC,id LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []state.OperationalReport{}
	for rows.Next() {
		var r state.OperationalReport
		var stamp string
		if err = rows.Scan(&r.ID, &r.Kind, &r.Name, &stamp); err != nil {
			return nil, err
		}
		r.ImportedAt, _ = time.Parse(time.RFC3339Nano, stamp)
		items = append(items, r)
	}
	return items, rows.Err()
}
func (s *Store) GetOperationalReport(id string) (*state.OperationalReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var r state.OperationalReport
	var stamp string
	err := s.db.QueryRow(`SELECT id,kind,name,imported_at,document FROM operational_reports WHERE id=?`, id).Scan(&r.ID, &r.Kind, &r.Name, &stamp, &r.Document)
	if err != nil {
		return nil, err
	}
	r.ImportedAt, _ = time.Parse(time.RFC3339Nano, stamp)
	return &r, nil
}
