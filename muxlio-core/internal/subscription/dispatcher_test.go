package subscription_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/subscription"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func definition(target string) subscription.Subscription {
	return subscription.Subscription{ID: "notify", Target: target, Secret: "0123456789abcdef", Enabled: true, EventTypes: []string{"run.succeeded", "run.failed", "run.waiting", "worker.*"}, RedactFields: []string{"private"}, MaxRetries: 1}
}

func TestSignedRetrySurvivesRestartAndRedactsInspection(t *testing.T) {
	var mu sync.Mutex
	var bodies [][]byte
	sub := definition("")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if err := sub.VerifyBody(body, r.Header.Get(sub.SignatureHeader)); err != nil {
			t.Error(err)
		}
		if err := sub.VerifyBody(append(append([]byte{}, body...), ' '), r.Header.Get(sub.SignatureHeader)); err == nil {
			t.Error("tampered body accepted")
		}
		if r.Header.Get("X-Muxlio-Event-ID") == "" {
			t.Error("event ID missing")
		}
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		w.WriteHeader(503)
	}))
	defer server.Close()
	sub.Target = server.URL
	must(t, sub.Normalize())
	path := filepath.Join(t.TempDir(), "subscriptions.db")
	s, err := sqlite.Open(path)
	must(t, err)
	must(t, s.CreateSubscription(sub))
	must(t, s.CreateRun(&state.Run{ID: "run", WorkflowName: "test", State: "running"}))
	must(t, s.AppendEvent("run", state.EventWorkerActivity, "worker", map[string]any{"private": "hidden-value", "nested": map[string]any{"password": "password-value", "private": "nested-value"}, "message": "Bearer bearer-value"}))
	must(t, s.SetRunState("run", "succeeded"))
	now := time.Now().UTC()
	d := subscription.Dispatcher{Store: s, Events: s}
	must(t, d.Tick(context.Background(), now))
	records, err := s.ListSubscriptionDeliveries(sub.ID, 100)
	must(t, err)
	if len(records) != 2 {
		t.Fatalf("records=%+v", records)
	}
	firstIDs := map[string]bool{}
	for _, record := range records {
		firstIDs[record.ID] = true
		if record.Attempts != 1 || record.State != subscription.StatePending {
			t.Fatalf("record=%+v", record)
		}
	}
	raw, err := json.Marshal(records)
	must(t, err)
	for _, secret := range []string{"hidden-value", "password-value", "nested-value", "bearer-value"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("inspection leaked %q", secret)
		}
	}
	must(t, s.Close())
	s, err = sqlite.Open(path)
	must(t, err)
	defer s.Close()
	restored, err := s.GetSubscription(sub.ID)
	must(t, err)
	if restored.Secret != sub.Secret {
		t.Fatal("signing key lost")
	}
	d = subscription.Dispatcher{Store: s, Events: s}
	must(t, d.Tick(context.Background(), now.Add(2*time.Second)))
	dead, err := s.ListDeadLetters(100)
	must(t, err)
	if len(dead) != 2 {
		t.Fatalf("deadletters=%+v", dead)
	}
	for _, record := range dead {
		if !firstIDs[record.ID] || record.Attempts != 2 {
			t.Fatalf("unstable record=%+v", record)
		}
	}
	// Exhausted delivery never changes the workflow outcome.
	run, err := s.GetRun("run")
	must(t, err)
	if run.State != "succeeded" {
		t.Fatalf("outcome=%s", run.State)
	}
	must(t, d.Tick(context.Background(), now.Add(time.Hour)))
	mu.Lock()
	if len(bodies) != 4 {
		t.Fatalf("attempts=%d", len(bodies))
	}
	captured := append([][]byte{}, bodies...)
	mu.Unlock()
	counts := map[string]int{}
	for _, body := range captured {
		var env subscription.Envelope
		must(t, json.Unmarshal(body, &env))
		counts[env.EventID]++
	}
	for id, n := range counts {
		if n != 2 {
			t.Fatalf("event %s delivered %d times", id, n)
		}
	}
	must(t, s.RequeueDeadLetter(dead[0].ID, now))
	must(t, d.Tick(context.Background(), now.Add(2*time.Hour)))
	again, err := s.GetDelivery(dead[0].ID)
	must(t, err)
	if again.EventID != dead[0].EventID {
		t.Fatal("requeue changed event ID")
	}
}

func TestBlockedSubscriberDoesNotBlockWorkflow(t *testing.T) {
	s, err := sqlite.Open(filepath.Join(t.TempDir(), "outage.db"))
	must(t, err)
	defer s.Close()
	sub := definition("local:blocked")
	must(t, s.CreateSubscription(sub))
	must(t, s.CreateRun(&state.Run{ID: "run", WorkflowName: "test", State: "running"}))
	must(t, s.AppendEvent("run", state.EventWorkerActivity, "worker", nil))
	entered := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := subscription.Dispatcher{Store: s, Events: s, Hooks: map[string]subscription.Hook{"blocked": func(ctx context.Context, body []byte, sig string) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	}}}
	done := make(chan error, 1)
	go func() { done <- d.Tick(ctx, time.Now()) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("hook not called")
	}
	finished := make(chan error, 1)
	go func() { finished <- s.SetRunState("run", "succeeded") }()
	select {
	case err := <-finished:
		must(t, err)
	case <-time.After(time.Second):
		t.Fatal("subscriber blocked workflow completion")
	}
	cancel()
	<-done
	run, err := s.GetRun("run")
	must(t, err)
	if run.State != "succeeded" {
		t.Fatal("outage changed outcome")
	}
}

func TestLocalHookAndRedirectIsolation(t *testing.T) {
	s, err := sqlite.Open(filepath.Join(t.TempDir(), "hooks.db"))
	must(t, err)
	defer s.Close()
	sub := definition("local:audit")
	sub.MaxRetries = 0
	must(t, sub.Normalize())
	must(t, s.CreateSubscription(sub))
	must(t, s.CreateRun(&state.Run{ID: "run", WorkflowName: "test", State: "running"}))
	must(t, s.AppendEvent("run", state.EventRunWaiting, "run", nil))
	calls := 0
	d := subscription.Dispatcher{Store: s, Events: s, Hooks: map[string]subscription.Hook{"audit": func(ctx context.Context, b []byte, sig string) error { calls++; return sub.VerifyBody(b, sig) }}}
	must(t, d.Tick(context.Background(), time.Now()))
	if calls != 1 {
		t.Fatalf("hook calls=%d", calls)
	}
	records, err := s.ListSubscriptionDeliveries(sub.ID, 100)
	must(t, err)
	if len(records) != 1 || records[0].State != subscription.StateDelivered {
		t.Fatalf("records=%+v", records)
	}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("redirect forwarded payload") }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer redirect.Close()
	sub.ID = "redirect"
	sub.Target = redirect.URL
	must(t, s.CreateSubscription(sub))
	must(t, d.Tick(context.Background(), time.Now()))
	dead, err := s.ListDeadLetters(100)
	must(t, err)
	if len(dead) != 1 || dead[0].Attempts != 1 {
		t.Fatalf("zero retries not enforced: %+v", dead)
	}
	// Cursor loss after enqueue (e.g. a crash) cannot create duplicate records.
	must(t, d.Tick(context.Background(), time.Now().Add(time.Hour)))
	records, err = s.ListSubscriptionDeliveries(sub.ID, 100)
	must(t, err)
	if len(records) != 1 {
		t.Fatal("fanout duplicated records")
	}
}

func TestInvalidDefinitions(t *testing.T) {
	for _, target := range []string{"https://", "http://user:password@example.com", "http://host/#fragment", "file:/tmp/script", "local:"} {
		t.Run(fmt.Sprintf("target_%s", target), func(t *testing.T) {
			sub := definition(target)
			if sub.Normalize() == nil {
				t.Fatal("invalid target accepted")
			}
		})
	}
}

func TestDisabledSubscriptionCannotStarveOtherDeliveries(t *testing.T) {
	s, err := sqlite.Open(filepath.Join(t.TempDir(), "disabled.db"))
	must(t, err)
	defer s.Close()
	sub := definition("local:audit")
	must(t, s.CreateSubscription(sub))
	for i := 0; i < 40; i++ {
		must(t, s.EnqueueDelivery(subscription.Delivery{ID: fmt.Sprintf("disabled-%02d", i), SubscriptionID: sub.ID, State: subscription.StatePending}))
	}
	must(t, s.DeleteSubscription(sub.ID))
	sub.ID = "active"
	must(t, s.CreateSubscription(sub))
	must(t, s.EnqueueDelivery(subscription.Delivery{ID: "z-active", SubscriptionID: sub.ID, State: subscription.StatePending, EventID: "run:1"}))
	calls := 0
	d := subscription.Dispatcher{Store: s, Events: s, Hooks: map[string]subscription.Hook{"audit": func(context.Context, []byte, string) error { calls++; return nil }}}
	must(t, d.Tick(context.Background(), time.Now()))
	if calls != 1 {
		t.Fatalf("active subscriber starved: calls=%d", calls)
	}
}

func TestUncooperativeHookCancellationIsBounded(t *testing.T) {
	s, err := sqlite.Open(filepath.Join(t.TempDir(), "cancel.db"))
	must(t, err)
	defer s.Close()
	sub := definition("local:stuck")
	must(t, s.CreateSubscription(sub))
	must(t, s.EnqueueDelivery(subscription.Delivery{ID: "one", SubscriptionID: sub.ID, State: subscription.StatePending, MaxRetries: 1}))
	entered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	d := subscription.Dispatcher{Store: s, Events: s, Hooks: map[string]subscription.Hook{"stuck": func(context.Context, []byte, string) error { close(entered); <-release; return nil }}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- d.Tick(ctx, time.Now()) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("hook not entered")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("hook blocked dispatcher shutdown")
	}
	// A retry does not start a second stuck invocation.
	record, err := s.GetDelivery("one")
	must(t, err)
	if record.Attempts != 0 || record.State != subscription.StatePending {
		t.Fatalf("shutdown consumed an attempt: %+v", record)
	}
	must(t, d.Tick(context.Background(), time.Now().Add(time.Hour)))
	must(t, d.Tick(context.Background(), time.Now().Add(2*time.Hour)))
	dead, err := s.ListDeadLetters(10)
	must(t, err)
	if len(dead) != 1 {
		t.Fatal("retry did not dead-letter")
	}
}

func TestEnqueueIsIdempotent(t *testing.T) {
	s, err := sqlite.Open(filepath.Join(t.TempDir(), "dedup.db"))
	must(t, err)
	defer s.Close()
	sub := definition("local:audit")
	must(t, s.CreateSubscription(sub))
	record := subscription.Delivery{ID: subscription.DeliveryID(sub.ID, "run", 1), SubscriptionID: sub.ID, EventID: subscription.EventID("run", 1), State: subscription.StatePending}
	must(t, s.EnqueueDelivery(record))
	must(t, s.RecordSubscriptionDelivery(record.ID, subscription.DeliveryResult{Succeeded: true}, nil, time.Now()))
	must(t, s.EnqueueDelivery(record))
	records, err := s.ListSubscriptionDeliveries(sub.ID, 100)
	must(t, err)
	if len(records) != 1 || records[0].State != subscription.StateDelivered || records[0].Attempts != 1 {
		t.Fatalf("replayed enqueue reset delivery: %+v", records)
	}
}

func TestBackoffStartsAfterAttemptCompletes(t *testing.T) {
	s, err := sqlite.Open(filepath.Join(t.TempDir(), "backoff.db"))
	must(t, err)
	defer s.Close()
	sub := definition("local:slow")
	must(t, s.CreateSubscription(sub))
	must(t, s.EnqueueDelivery(subscription.Delivery{ID: "slow", SubscriptionID: sub.ID, State: subscription.StatePending, MaxRetries: 1}))
	d := subscription.Dispatcher{Store: s, Events: s, Hooks: map[string]subscription.Hook{"slow": func(context.Context, []byte, string) error {
		time.Sleep(40 * time.Millisecond)
		return fmt.Errorf("unavailable")
	}}}
	now := time.Now().UTC()
	must(t, d.Tick(context.Background(), now))
	record, err := s.GetDelivery("slow")
	must(t, err)
	if record.NextAttemptAt == nil || record.NextAttemptAt.Before(now.Add(time.Second+30*time.Millisecond)) {
		t.Fatalf("backoff was measured before attempt completion: %+v", record)
	}
}

func TestDeliveryPreservesJSONNumbersAcrossPersistenceAndRetry(t *testing.T) {
	s, err := sqlite.Open(filepath.Join(t.TempDir(), "numbers.db"))
	must(t, err)
	defer s.Close()
	sub := definition("local:numbers")
	must(t, s.CreateSubscription(sub))
	must(t, s.CreateRun(&state.Run{ID: "run", WorkflowName: "test", State: "running"}))
	must(t, s.AppendEvent("run", state.EventWorkerActivity, "worker", map[string]any{
		"integer": json.Number("9007199254740993"), "decimal": json.Number("0.1234567890123456789012345"),
	}))
	var bodies [][]byte
	d := subscription.Dispatcher{Store: s, Events: s, Hooks: map[string]subscription.Hook{"numbers": func(_ context.Context, body []byte, signature string) error {
		if err := sub.VerifyBody(body, signature); err != nil {
			return err
		}
		bodies = append(bodies, append([]byte(nil), body...))
		return fmt.Errorf("retry")
	}}}
	must(t, sub.Normalize())
	now := time.Now().UTC()
	must(t, d.Tick(context.Background(), now))
	records, err := s.ListSubscriptionDeliveries(sub.ID, 10)
	must(t, err)
	raw, err := json.Marshal(records)
	must(t, err)
	for _, number := range []string{"9007199254740993", "0.1234567890123456789012345"} {
		if !bytes.Contains(raw, []byte(number)) {
			t.Fatalf("inspection changed number %s: %s", number, raw)
		}
	}
	must(t, d.Tick(context.Background(), now.Add(time.Hour)))
	if len(bodies) != 2 || !bytes.Equal(bodies[0], bodies[1]) {
		t.Fatalf("retry changed signed body: %q", bodies)
	}
	for _, number := range []string{"9007199254740993", "0.1234567890123456789012345"} {
		if !bytes.Contains(bodies[0], []byte(number)) {
			t.Fatalf("delivery changed number %s: %s", number, bodies[0])
		}
	}
}
