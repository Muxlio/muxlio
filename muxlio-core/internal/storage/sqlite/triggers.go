package sqlite

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/trigger"
)

var _ trigger.Store = (*Store)(nil)

// triggerSummary holds the non-secret fields persisted in definition_json.
type triggerSummary struct {
	ID                   string          `json:"id"`
	WorkflowDigest       string          `json:"workflowDigest"`
	SignatureScheme      string          `json:"signatureScheme"`
	SignatureHeader      string          `json:"signatureHeader"`
	SignaturePrefix      string          `json:"signaturePrefix,omitempty"`
	SignatureEncoding    string          `json:"signatureEncoding,omitempty"`
	InputMapping         json.RawMessage `json:"inputMapping,omitempty"`
	PayloadLimitBytes    int             `json:"payloadLimitBytes"`
	DeduplicationSeconds int             `json:"deduplicationSeconds"`
	Enabled              bool            `json:"enabled"`
}

// CreateTrigger persists a normalized trigger. The secret is stored in a
// dedicated column so it never appears in definition_json or API responses.
func (s *Store) CreateTrigger(t trigger.Trigger) error {
	if err := t.Normalize(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	summary := triggerSummary{
		ID: t.ID, WorkflowDigest: t.WorkflowDigest,
		SignatureScheme: t.SignatureScheme, SignatureHeader: t.SignatureHeader,
		SignaturePrefix: t.SignaturePrefix, SignatureEncoding: t.SignatureEncoding,
		InputMapping: t.InputMapping, PayloadLimitBytes: t.PayloadLimitBytes,
		DeduplicationSeconds: t.DeduplicationSeconds, Enabled: t.Enabled,
	}
	raw, err := json.Marshal(summary)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO triggers(id, definition_json, secret, created_at) VALUES (?,?,?,?)`, t.ID, raw, t.Secret, rfc3339(time.Now()))
	if err != nil {
		return fmt.Errorf("muxlio: trigger %q already exists or cannot be stored: %w", t.ID, err)
	}
	return nil
}

func (s *Store) GetTrigger(id string) (trigger.Trigger, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw []byte
	var secret, created string
	err := s.db.QueryRow(`SELECT definition_json, secret, created_at FROM triggers WHERE id=?`, id).Scan(&raw, &secret, &created)
	if err == sql.ErrNoRows {
		return trigger.Trigger{}, fmt.Errorf("muxlio: trigger %q not found", id)
	}
	if err != nil {
		return trigger.Trigger{}, err
	}
	var summary triggerSummary
	if err := json.Unmarshal(raw, &summary); err != nil {
		return trigger.Trigger{}, err
	}
	return trigger.Trigger{
		ID: summary.ID, WorkflowDigest: summary.WorkflowDigest, Secret: secret,
		SignatureScheme: summary.SignatureScheme, SignatureHeader: summary.SignatureHeader,
		SignaturePrefix: summary.SignaturePrefix, SignatureEncoding: summary.SignatureEncoding,
		InputMapping: summary.InputMapping, PayloadLimitBytes: summary.PayloadLimitBytes,
		DeduplicationSeconds: summary.DeduplicationSeconds, Enabled: summary.Enabled,
	}, nil
}

func (s *Store) ListTriggers() ([]trigger.Trigger, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT definition_json, secret FROM triggers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []trigger.Trigger
	for rows.Next() {
		var raw []byte
		var secret string
		if err := rows.Scan(&raw, &secret); err != nil {
			return nil, err
		}
		var summary triggerSummary
		if err := json.Unmarshal(raw, &summary); err != nil {
			return nil, err
		}
		out = append(out, trigger.Trigger{
			ID: summary.ID, WorkflowDigest: summary.WorkflowDigest, Secret: secret,
			SignatureScheme: summary.SignatureScheme, SignatureHeader: summary.SignatureHeader,
			SignaturePrefix: summary.SignaturePrefix, SignatureEncoding: summary.SignatureEncoding,
			InputMapping: summary.InputMapping, PayloadLimitBytes: summary.PayloadLimitBytes,
			DeduplicationSeconds: summary.DeduplicationSeconds, Enabled: summary.Enabled,
		})
	}
	return out, rows.Err()
}

func newWebhookRunID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("webhook_%d", time.Now().UnixNano())
	}
	return "webhook_" + hex.EncodeToString(b[:])
}

// DeliverWebhook atomically deduplicates and creates a pinned run for an
// inbound delivery. When IdempotencyKey is non-empty, an earlier delivered run
// for the same trigger+key within the deduplication window is returned without
// creating a new run. The created run pins the supplied immutable plan bytes
// and digest so a trigger always records which plan version was used. The
// write lock is held across dedup-check and run creation so concurrent
// deliveries with the same key cannot both create runs.
func (s *Store) DeliverWebhook(req trigger.WebhookDeliveryRequest) (trigger.WebhookDeliveryResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return trigger.WebhookDeliveryResult{}, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	if req.IdempotencyKey != "" {
		var runID, digest, ts string
		err := tx.QueryRow(`SELECT run_id, workflow_digest, timestamp FROM trigger_deliveries WHERE trigger_id=? AND idempotency_key=? AND state='delivered' ORDER BY id DESC LIMIT 1`, req.TriggerID, req.IdempotencyKey).Scan(&runID, &digest, &ts)
		if err == nil {
			deliveredAt, perr := time.Parse(time.RFC3339Nano, ts)
			if perr != nil {
				return trigger.WebhookDeliveryResult{}, perr
			}
			if !now.After(deliveredAt.Add(time.Duration(req.DeduplicationSeconds) * time.Second)) {
				if _, err := tx.Exec(`INSERT INTO trigger_deliveries(trigger_id, idempotency_key, run_id, workflow_digest, state, timestamp) VALUES (?,?,?,?,?,?)`, req.TriggerID, req.IdempotencyKey, runID, digest, trigger.DeliveryDuplicate, rfc3339(now)); err != nil {
					return trigger.WebhookDeliveryResult{}, err
				}
				return trigger.WebhookDeliveryResult{RunID: runID, WorkflowDigest: digest, Duplicate: true}, tx.Commit()
			}
		} else if err != sql.ErrNoRows {
			return trigger.WebhookDeliveryResult{}, err
		}
	}
	runID := newWebhookRunID()
	if _, err = tx.Exec(`INSERT INTO runs(id, workflow_name, state, input_json, error, created_at, updated_at, plan_json, plan_digest) VALUES (?,?,'created',?,'',?,?,?,?)`, runID, req.WorkflowName, string(req.Input), rfc3339(now), rfc3339(now), req.PlanJSON, req.WorkflowDigest); err != nil {
		return trigger.WebhookDeliveryResult{}, err
	}
	if err := appendEvent(tx, runID, state.EventRunCreated, runID, map[string]any{"triggerId": req.TriggerID, "idempotencyKey": req.IdempotencyKey}); err != nil {
		return trigger.WebhookDeliveryResult{}, err
	}
	if _, err = tx.Exec(`INSERT INTO trigger_deliveries(trigger_id, idempotency_key, run_id, workflow_digest, state, timestamp) VALUES (?,?,?,?,?,?)`, req.TriggerID, req.IdempotencyKey, runID, req.WorkflowDigest, trigger.DeliveryDelivered, rfc3339(now)); err != nil {
		return trigger.WebhookDeliveryResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return trigger.WebhookDeliveryResult{}, err
	}
	return trigger.WebhookDeliveryResult{RunID: runID, WorkflowDigest: req.WorkflowDigest, Duplicate: false}, nil
}

// RecordDelivery records a non-creating delivery (a rejected or duplicate
// attempt) for audit without creating a run.
func (s *Store) RecordDelivery(d trigger.Delivery) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d.Timestamp.IsZero() {
		d.Timestamp = time.Now()
	}
	_, err := s.db.Exec(`INSERT INTO trigger_deliveries(trigger_id, idempotency_key, run_id, workflow_digest, state, reason, timestamp) VALUES (?,?,NULLIF(?,''),?,?,?,?)`, d.TriggerID, d.IdempotencyKey, d.RunID, d.WorkflowDigest, d.State, d.Reason, rfc3339(d.Timestamp))
	return err
}

func (s *Store) ListDeliveries(triggerID string) ([]trigger.Delivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT idempotency_key, COALESCE(run_id,''), workflow_digest, state, reason, timestamp FROM trigger_deliveries WHERE trigger_id=? ORDER BY id DESC`, triggerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []trigger.Delivery{}
	for rows.Next() {
		var d trigger.Delivery
		var ts string
		if err := rows.Scan(&d.IdempotencyKey, &d.RunID, &d.WorkflowDigest, &d.State, &d.Reason, &ts); err != nil {
			return nil, err
		}
		d.TriggerID = triggerID
		d.Timestamp, err = time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
