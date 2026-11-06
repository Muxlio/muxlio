package sqlite

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/muxlio/muxlio-core/internal/subscription"
)

var _ subscription.Store = (*Store)(nil)

func (s *Store) CreateSubscription(sub subscription.Subscription) error {
	if err := sub.Normalize(); err != nil {
		return err
	}
	raw, err := json.Marshal(sub)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.Exec(`INSERT INTO subscriptions(id, definition_json, secret) VALUES (?,?,?)`, sub.ID, raw, sub.Secret)
	return err
}
func (s *Store) GetSubscription(id string) (subscription.Subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw []byte
	var sub subscription.Subscription
	err := s.db.QueryRow(`SELECT definition_json, secret FROM subscriptions WHERE id=?`, id).Scan(&raw, &sub.Secret)
	if err != nil {
		return sub, err
	}
	err = json.Unmarshal(raw, &sub)
	return sub, err
}
func (s *Store) ListSubscriptions() ([]subscription.Subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT definition_json, secret FROM subscriptions ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []subscription.Subscription{}
	for rows.Next() {
		var raw []byte
		var sub subscription.Subscription
		if err := rows.Scan(&raw, &sub.Secret); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &sub); err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

// DeleteSubscription disables future fanout and pending delivery while retaining
// the identity and history. IDs cannot be reused for a different receiver.
func (s *Store) DeleteSubscription(id string) error {
	sub, err := s.GetSubscription(id)
	if err != nil {
		return err
	}
	sub.Enabled = false
	raw, err := json.Marshal(sub)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.Exec(`UPDATE subscriptions SET definition_json=? WHERE id=?`, raw, id)
	return err
}
func (s *Store) EnqueueDelivery(d subscription.Delivery) error {
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.Exec(`INSERT INTO subscription_deliveries(id,subscription_id,state,due,record_json) VALUES (?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, d.ID, d.SubscriptionID, d.State, subscriptionDue(d), raw)
	return err
}
func subscriptionDue(d subscription.Delivery) int64 {
	if d.NextAttemptAt != nil {
		return d.NextAttemptAt.UnixNano()
	}
	return 0
}
func (s *Store) GetDelivery(id string) (subscription.Delivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return readSubscriptionDelivery(s.db, id)
}
func readSubscriptionDelivery(db *sql.DB, id string) (subscription.Delivery, error) {
	var raw []byte
	var d subscription.Delivery
	err := db.QueryRow(`SELECT record_json FROM subscription_deliveries WHERE id=?`, id).Scan(&raw)
	if err != nil {
		return d, err
	}
	err = decodeSubscriptionDelivery(raw, &d)
	return d, err
}

func decodeSubscriptionDelivery(raw []byte, d *subscription.Delivery) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return dec.Decode(d)
}
func (s *Store) subscriptionDeliveries(where string, args ...any) ([]subscription.Delivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT record_json FROM subscription_deliveries `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []subscription.Delivery{}
	for rows.Next() {
		var raw []byte
		var d subscription.Delivery
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := decodeSubscriptionDelivery(raw, &d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func subscriptionLimit(n int) int {
	if n <= 0 || n > 1000 {
		return 1000
	}
	return n
}
func (s *Store) ListDueDeliveries(now time.Time, limit int) ([]subscription.Delivery, error) {
	return s.subscriptionDeliveries(`WHERE state IN ('pending','requeued') AND due<=? AND subscription_id IN (SELECT id FROM subscriptions WHERE json_extract(definition_json,'$.enabled')=1) ORDER BY due,id LIMIT ?`, now.UnixNano(), subscriptionLimit(limit))
}
func (s *Store) ListSubscriptionDeliveries(id string, limit int) ([]subscription.Delivery, error) {
	return s.subscriptionDeliveries(`WHERE subscription_id=? ORDER BY rowid DESC LIMIT ?`, id, subscriptionLimit(limit))
}
func (s *Store) ListDeadLetters(limit int) ([]subscription.Delivery, error) {
	return s.subscriptionDeliveries(`WHERE state='deadletter' ORDER BY rowid DESC LIMIT ?`, subscriptionLimit(limit))
}
func (s *Store) saveSubscriptionDelivery(d subscription.Delivery) error {
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE subscription_deliveries SET state=?,due=?,record_json=? WHERE id=?`, d.State, subscriptionDue(d), raw, d.ID)
	return err
}
func (s *Store) RecordSubscriptionDelivery(id string, result subscription.DeliveryResult, next *time.Time, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := readSubscriptionDelivery(s.db, id)
	if err != nil {
		return err
	}
	if d.State != subscription.StatePending && d.State != subscription.StateRequeued {
		return nil
	}
	d.Attempts++
	d.NextAttemptAt = nil
	if result.Succeeded {
		d.State = subscription.StateDelivered
		d.DeliveredAt = &now
		d.LastError = ""
	} else {
		// Never persist receiver responses, target URLs or hook errors: they may
		// contain credentials or echo the original payload.
		d.LastError = "delivery failed"
		if d.Attempts > d.MaxRetries || next == nil {
			d.State = subscription.StateDeadLetter
		} else {
			d.State = subscription.StatePending
			d.NextAttemptAt = next
		}
	}
	return s.saveSubscriptionDelivery(d)
}
func (s *Store) RequeueDeadLetter(id string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := readSubscriptionDelivery(s.db, id)
	if err != nil {
		return err
	}
	if d.State != subscription.StateDeadLetter {
		return fmt.Errorf("delivery is not dead-lettered")
	}
	d.State = subscription.StateRequeued
	d.Attempts = 0
	d.NextAttemptAt = &now
	d.LastError = ""
	return s.saveSubscriptionDelivery(d)
}
func (s *Store) GetCursor(id string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var seq int
	err := s.db.QueryRow(`SELECT seq FROM subscription_cursors WHERE id=?`, id).Scan(&seq)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return seq, err
}
func (s *Store) SetCursor(id string, seq int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO subscription_cursors(id,seq) VALUES (?,?) ON CONFLICT(id) DO UPDATE SET seq=MAX(seq,excluded.seq)`, id, seq)
	return err
}
