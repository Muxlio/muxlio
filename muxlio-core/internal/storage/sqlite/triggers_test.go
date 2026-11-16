package sqlite

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/trigger"
)

func TestWebhookDeduplicationPersistsAndExpires(t *testing.T) {
	path := filepath.Join(t.TempDir(), "webhooks.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	tr := trigger.Trigger{ID: "one", WorkflowDigest: "pin", Secret: "0123456789abcdef", Enabled: true}
	if err := tr.Normalize(); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateTrigger(tr); err != nil {
		t.Fatal(err)
	}
	tr.ID = "two"
	if err := s.CreateTrigger(tr); err != nil {
		t.Fatal(err)
	}
	req := trigger.WebhookDeliveryRequest{TriggerID: "one", IdempotencyKey: "delivery", WorkflowDigest: "pin", WorkflowName: "test", PlanJSON: []byte(`{"digest":"pin"}`), Input: json.RawMessage(`false`), DeduplicationSeconds: 10}
	first, err := s.DeliverWebhook(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	stored, err := s.GetTrigger("one")
	if err != nil || stored.Secret != tr.Secret {
		t.Fatalf("lost authentication config: %+v %v", stored, err)
	}
	duplicate, err := s.DeliverWebhook(req)
	if err != nil || !duplicate.Duplicate || duplicate.RunID != first.RunID {
		t.Fatalf("duplicate=%+v %v", duplicate, err)
	}
	req.TriggerID = "two"
	other, err := s.DeliverWebhook(req)
	if err != nil || other.Duplicate || other.RunID == first.RunID {
		t.Fatalf("trigger namespace collided: %+v %v", other, err)
	}
	// Expire the delivered entry without sleeping. Duplicate retries must not
	// extend the window, which starts at the original accepted delivery.
	if _, err := s.db.Exec(`UPDATE trigger_deliveries SET timestamp=? WHERE trigger_id='one' AND state='delivered'`, rfc3339(time.Now().Add(-11*time.Second))); err != nil {
		t.Fatal(err)
	}
	req.TriggerID = "one"
	next, err := s.DeliverWebhook(req)
	if err != nil || next.Duplicate || next.RunID == first.RunID {
		t.Fatalf("expired key not reusable: %+v %v", next, err)
	}
	// A failed audit insert must roll back the run in the same transaction.
	req.TriggerID = "missing"
	if _, err := s.DeliverWebhook(req); err == nil {
		t.Fatal("expected foreign-key failure")
	}
	runs, err := s.ListRuns()
	if err != nil || len(runs) != 3 {
		t.Fatalf("partial run committed: %d %v", len(runs), err)
	}
}

func TestWebhookNumericInputReadableAfterReopen(t *testing.T) {
	for _, input := range []string{`9007199254740993`, `1e400`, `{"values":[9007199254740993,1e400,7,0]}`} {
		t.Run(input, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "webhooks.db")
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { s.Close() }()
			tr := trigger.Trigger{ID: "numbers", WorkflowDigest: "pin", Secret: "0123456789abcdef", Enabled: true}
			if err := tr.Normalize(); err != nil {
				t.Fatal(err)
			}
			if err := s.CreateTrigger(tr); err != nil {
				t.Fatal(err)
			}
			req := trigger.WebhookDeliveryRequest{TriggerID: tr.ID, IdempotencyKey: "delivery", WorkflowDigest: "pin", WorkflowName: "numbers", PlanJSON: []byte(`{"digest":"pin"}`), Input: json.RawMessage(input), DeduplicationSeconds: 60}
			first, err := s.DeliverWebhook(req)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			retry, err := s.DeliverWebhook(req)
			if err != nil || !retry.Duplicate || retry.RunID != first.RunID {
				t.Fatalf("retry=%+v: %v", retry, err)
			}
			runs, err := s.ListRuns()
			if err != nil {
				t.Fatal(err)
			}
			if len(runs) != 1 {
				t.Fatalf("runs=%d", len(runs))
			}
			got, err := json.Marshal(runs[0].Input)
			if err != nil || string(got) != input {
				t.Fatalf("input=%s want %s: %v", got, input, err)
			}
		})
	}
}

func TestWebhookBodyReplayAndKeyConflict(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "webhooks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tr := trigger.Trigger{ID: "one", WorkflowDigest: "pin", Secret: "0123456789abcdef", Enabled: true}
	if err := tr.Normalize(); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateTrigger(tr); err != nil {
		t.Fatal(err)
	}
	req := trigger.WebhookDeliveryRequest{TriggerID: "one", IdempotencyKey: "event", WorkflowDigest: "pin", PlanJSON: []byte(`{"digest":"pin"}`), Input: json.RawMessage(`{"n":1}`), BodyDigest: "body-a", DeduplicationSeconds: 60}
	first, err := s.DeliverWebhook(req)
	if err != nil {
		t.Fatal(err)
	}
	req.IdempotencyKey = "fresh-unsigned-key"
	replay, err := s.DeliverWebhook(req)
	if err != nil || !replay.Duplicate || replay.RunID != first.RunID {
		t.Fatalf("replay=%+v %v", replay, err)
	}
	req.IdempotencyKey = "event"
	req.BodyDigest = "body-b"
	if _, err := s.DeliverWebhook(req); !errors.Is(err, trigger.ErrIdempotencyConflict) {
		t.Fatalf("err=%v", err)
	}
	// A previously accepted body must not bypass a conflicting key.
	req.IdempotencyKey = "other-event"
	second, err := s.DeliverWebhook(req)
	if err != nil || second.Duplicate {
		t.Fatalf("second=%+v %v", second, err)
	}
	for _, key := range []string{"event", "fresh-unsigned-key"} {
		req.IdempotencyKey = key
		if _, err := s.DeliverWebhook(req); !errors.Is(err, trigger.ErrIdempotencyConflict) {
			t.Fatalf("key %q bypassed conflict with existing body: %v", key, err)
		}
	}
	// Duplicate aliases retain the original acceptance window, even if a
	// duplicate was recorded later. Reuse after expiry creates a new run.
	if _, err := s.db.Exec(`UPDATE trigger_deliveries SET timestamp=? WHERE trigger_id='one' AND state='delivered'`, rfc3339(time.Now().Add(-time.Minute-time.Second))); err != nil {
		t.Fatal(err)
	}
	req.IdempotencyKey = "fresh-unsigned-key"
	next, err := s.DeliverWebhook(req)
	if err != nil || next.Duplicate || next.RunID == first.RunID || next.RunID == second.RunID {
		t.Fatalf("expired alias=%+v %v", next, err)
	}
}
