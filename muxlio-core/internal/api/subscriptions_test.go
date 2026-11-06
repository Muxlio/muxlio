package api

import (
	"bytes"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

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
