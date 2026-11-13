package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/subscription"
)

func TestSubscriptionAuthorizationAndSecretIsolation(t *testing.T) {
	s, err := sqlite.Open(filepath.Join(t.TempDir(), "subscriptions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	router := WithAuth(NewRouter(s, nil), "admin", "worker", AccessGrant{Token: "scoped", RunID: "r", Permissions: []string{"read", "control"}})
	request := func(method, path, body, token string, status int) []byte {
		t.Helper()
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.RemoteAddr = "192.0.2.1:1234"
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != status {
			t.Fatalf("%s %s: %d want %d: %s", method, path, rec.Code, status, rec.Body.String())
		}
		return rec.Body.Bytes()
	}
	const definition = `{"id":"notify","target":"https://example.com/hook","eventTypes":["run.*","worker.*"],"secret":"0123456789abcdef","maxRetries":0}`
	for _, token := range []string{"", "worker", "scoped"} {
		status := 403
		if token == "" {
			status = 401
		}
		for _, endpoint := range []struct{ method, path string }{{"POST", "/v1/subscriptions"}, {"GET", "/v1/subscriptions"}, {"GET", "/v1/subscriptions/notify"}, {"GET", "/v1/subscriptions/notify/deliveries"}, {"GET", "/v1/subscriptions/deadletters"}, {"POST", "/v1/subscriptions/deliveries/a/requeue"}, {"DELETE", "/v1/subscriptions/notify"}} {
			request(endpoint.method, endpoint.path, definition, token, status)
		}
	}
	created := request("POST", "/v1/subscriptions", definition, "admin", 201)
	for _, body := range [][]byte{created, request("GET", "/v1/subscriptions", "", "admin", 200), request("GET", "/v1/subscriptions/notify", "", "admin", 200)} {
		if bytes.Contains(body, []byte("0123456789abcdef")) || bytes.Contains(body, []byte(`"secret"`)) {
			t.Fatalf("secret leaked: %s", body)
		}
	}
	sub, err := s.GetSubscription("notify")
	if err != nil || sub.MaxRetries != 0 || !sub.Enabled {
		t.Fatalf("defaults=%+v %v", sub, err)
	}
	request("POST", "/v1/subscriptions", definition+` {}`, "admin", 400)
	request("POST", "/v1/subscriptions", `{"id":"bad","secret":"tiny"}`, "admin", 400)
	d := subscription.Delivery{ID: "notify:r:1", SubscriptionID: "notify", EventID: "r:1", State: subscription.StatePending}
	if err := s.EnqueueDelivery(d); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordSubscriptionDelivery(d.ID, subscription.DeliveryResult{}, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	request("GET", "/v1/subscriptions/deadletters", "", "admin", 200)
	request("POST", "/v1/subscriptions/deliveries/notify:r:1/requeue", "", "admin", 200)
	request("POST", "/v1/subscriptions/deliveries/notify:r:1/requeue", "", "admin", 400)
	request("DELETE", "/v1/subscriptions/notify", "", "admin", 204)
	sub, err = s.GetSubscription("notify")
	if err != nil || sub.Enabled {
		t.Fatal("subscription still enabled")
	}
	request("POST", "/v1/subscriptions", definition, "admin", 400)
}

// Exercise the optional notification surface through the existing management
// API, real durable storage and HTTP transport, independently of any client.
func TestRunNotificationsAreDurableAndMetadataOnly(t *testing.T) {
	var bodies [][]byte
	var sub subscription.Subscription
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if err := sub.VerifyBody(body, r.Header.Get(sub.SignatureHeader)); err != nil {
			t.Error(err)
		}
		var env subscription.Envelope
		if err := json.Unmarshal(body, &env); err != nil {
			t.Error(err)
		}
		if r.Header.Get("X-Muxlio-Event-ID") != env.EventID {
			t.Error("event header differs from provenance")
		}
		bodies = append(bodies, body)
		if len(bodies) <= 4 {
			w.WriteHeader(503)
		}
	}))
	defer receiver.Close()
	path := filepath.Join(t.TempDir(), "notifications.db")
	s, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	router := NewRouter(s, nil)
	definition, _ := json.Marshal(map[string]any{
		"id": "notifications", "target": receiver.URL, "secret": "0123456789abcdef",
		"payloadMode": "notification", "eventTypes": []string{"*"}, "maxRetries": 1,
	})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/subscriptions", bytes.NewReader(definition)))
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	sub, err = s.GetSubscription("notifications")
	if err != nil {
		t.Fatal(err)
	}
	if sub.PayloadMode != "notification" {
		t.Fatal("notification configuration lost")
	}
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	check(s.CreateRun(&state.Run{ID: "run", WorkflowName: "private workflow", State: "running"}))
	// None of these arbitrary strings is credential-shaped. Field redaction
	// alone is insufficient to prevent private transcript disclosure.
	private := map[string]any{"message": "private transcript", "output": "private answer", "error": "private failure"}
	check(s.AppendEvent("run", state.EventWorkerActivity, "activation", private))
	check(s.CreateInputRequest(&state.InputRequest{RunID: "run", RequestID: "request", Name: "private prompt"}))
	check(s.RecordExecutionAttempt(state.ExecutionAttempt{ID: "attempt", RunID: "run", State: "awaiting_reconciliation", AwaitReconciliationReason: "private reason"}))
	check(s.AppendEvent("run", state.EventRunFailed, "run", private))
	check(s.AppendEvent("run", state.EventRunSucceeded, "run", private))
	now := time.Now().UTC()
	dispatcher := subscription.Dispatcher{Store: s, Events: s}
	check(dispatcher.Tick(context.Background(), now))
	records, err := s.ListSubscriptionDeliveries(sub.ID, 100)
	check(err)
	if len(records) != 4 || len(bodies) != 4 {
		t.Fatalf("records=%d requests=%d", len(records), len(bodies))
	}
	raw, err := json.Marshal(records)
	check(err)
	if bytes.Contains(raw, []byte("private")) {
		t.Fatalf("durable notification leaked private content: %s", raw)
	}
	check(s.Close())
	s, err = sqlite.Open(path)
	check(err)
	dispatcher = subscription.Dispatcher{Store: s, Events: s}
	check(dispatcher.Tick(context.Background(), now.Add(time.Minute)))
	check(dispatcher.Tick(context.Background(), now.Add(time.Hour)))
	if len(bodies) != 8 {
		t.Fatalf("expected four deliveries and four retries, got %d", len(bodies))
	}
	expected := map[string]string{
		state.EventInputRequired: "input_required", state.EventReconciliationRequired: "reconciliation_required",
		state.EventRunFailed: "failure", state.EventRunSucceeded: "completion",
	}
	seen := map[string][]byte{}
	for _, body := range bodies {
		var env subscription.Envelope
		check(json.Unmarshal(body, &env))
		kind, ok := expected[env.EventType]
		if !ok {
			t.Fatalf("unexpected event: %+v", env)
		}
		want := map[string]any{"version": float64(1), "source": "muxlio.run-event", "kind": kind,
			"links": map[string]any{"run": "/runs/run", "status": "/v1/runs/run/summary"}}
		if !reflect.DeepEqual(env.Payload, want) {
			t.Fatalf("payload=%+v want=%+v", env.Payload, want)
		}
		if env.EventID != subscription.EventID("run", env.Seq) || env.Timestamp.IsZero() {
			t.Fatalf("provenance=%+v", env)
		}
		if previous, ok := seen[env.EventID]; ok && !bytes.Equal(previous, body) {
			t.Fatal("retry changed the durable body")
		}
		seen[env.EventID] = body
	}
	if len(seen) != 4 {
		t.Fatalf("unique events=%d", len(seen))
	}
	records, err = s.ListSubscriptionDeliveries(sub.ID, 100)
	check(err)
	for _, record := range records {
		if record.State != subscription.StateDelivered || record.Attempts != 2 {
			t.Fatalf("delivery=%+v", record)
		}
	}
	// Status remains readable from the same API used by any MCP bridge after
	// notifications, including unresolved input/reconciliation evidence.
	router = NewRouter(s, nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/runs/run/summary", nil))
	if rec.Code != 200 {
		t.Fatalf("status: %d %s", rec.Code, rec.Body.String())
	}
	var summary map[string]any
	check(json.Unmarshal(rec.Body.Bytes(), &summary))
	if summary["terminal"] != false {
		t.Fatalf("pending work reported terminal: %s", rec.Body.String())
	}
}
