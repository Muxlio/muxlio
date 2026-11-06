package api

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
)

func TestAuthenticatedPinnedWebhooks(t *testing.T) {
	s, err := sqlite.Open(filepath.Join(t.TempDir(), "coordinator.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	router := WithAuth(NewRouter(s, nil), "admin", "worker", AccessGrant{Token: "scoped", RunID: "r", Permissions: []string{"read", "control"}})
	request := func(method, path string, body []byte, token, signature, key string, status int) []byte {
		t.Helper()
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		req.RemoteAddr = "192.0.2.1:1234"
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.Header.Set("X-Event-Signature", signature)
		req.Header.Set("Idempotency-Key", key)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != status {
			t.Errorf("%s %s: %d want %d: %s", method, path, rec.Code, status, rec.Body.String())
		}
		return rec.Body.Bytes()
	}
	p := &plan.Plan{Name: "webhook", IRVersion: "1.0", Root: "root", InputSchema: json.RawMessage(`{"type":"object","properties":{"count":{"type":"integer"}},"required":["count"]}`), Subgraphs: map[string]plan.Subgraph{"root": {Nodes: []plan.Node{{ID: "task", Kind: plan.KindExecute, Executor: "test/task"}}}}}
	digest, err := p.ComputeDigest()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(p)
	request("POST", "/v1/workflow-versions", raw, "admin", "", "", 201)
	const secret = "0123456789abcdef"
	request("POST", "/v1/triggers", []byte(`{"id":"missing-version","workflowDigest":"missing","secret":"0123456789abcdef"}`), "admin", "", "", 400)
	request("POST", "/v1/triggers", []byte(`{"id":"bad-header","workflowDigest":"`+digest+`","secret":"0123456789abcdef","signatureHeader":"bad header"}`), "admin", "", "", 400)
	definition, _ := json.Marshal(map[string]any{"id": "deploy", "workflowDigest": digest, "secret": secret, "signatureHeader": "X-Event-Signature", "signaturePrefix": "sha256=", "inputMapping": map[string]string{"count": "{{/event/count}}"}, "payloadLimitBytes": 128})
	for _, token := range []string{"", "worker", "scoped"} {
		status := 403
		if token == "" {
			status = 401
		}
		request("POST", "/v1/triggers", definition, token, "", "", status)
		request("GET", "/v1/triggers", nil, token, "", "", status)
		request("GET", "/v1/triggers/deploy/deliveries", nil, token, "", "", status)
	}
	created := request("POST", "/v1/triggers", definition, "admin", "", "", 201)
	for _, body := range [][]byte{created, request("GET", "/v1/triggers", nil, "admin", "", "", 200), request("GET", "/v1/triggers/deploy", nil, "admin", "", "", 200)} {
		if bytes.Contains(body, []byte(secret)) || bytes.Contains(body, []byte(`"secret"`)) {
			t.Fatalf("secret exposed: %s", body)
		}
	}
	request("POST", "/v1/triggers", definition, "admin", "", "", 400)
	request("POST", "/v1/triggers", append(definition, []byte(` {}`)...), "admin", "", "", 400)
	request("POST", "/v1/triggers", []byte(`{"id":"no-auth","workflowDigest":"missing"}`), "admin", "", "", 400)
	sign := func(body []byte, secret string) string {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		return "sha256=" + hex.EncodeToString(mac.Sum(nil))
	}
	valid := []byte(`{"event":{"count":7}}`)
	request("POST", "/webhooks/deploy", valid, "admin", "", "event-1", 401)
	request("POST", "/webhooks/deploy", valid, "", sign(valid, "other-trigger-secret"), "event-1", 401)
	request("POST", "/webhooks/deploy", []byte(`{"event":{"count":8}}`), "", sign(valid, secret), "event-1", 401)
	for _, body := range [][]byte{[]byte(`{`), []byte(`{} {}`), []byte(`{}`), []byte(`{"event":{"count":"bad"}}`)} {
		request("POST", "/webhooks/deploy", body, "", sign(body, secret), "event-1", 400)
	}
	oversized := bytes.Repeat([]byte(" "), 129)
	request("POST", "/webhooks/deploy", oversized, "", sign(oversized, secret), "event-1", 413)
	runs, _ := s.ListRuns()
	if len(runs) != 0 {
		t.Fatal("rejected deliveries created runs")
	}
	// Valid signed payloads cannot expand beyond the mapped-input bound.
	expandedDefinition, _ := json.Marshal(map[string]any{"id": "expanded", "workflowDigest": digest, "secret": secret, "signatureHeader": "X-Event-Signature", "signaturePrefix": "sha256=", "inputMapping": strings.Repeat("{{/text}}", 100000)})
	request("POST", "/v1/triggers", expandedDefinition, "admin", "", "", 201)
	expandedBody := []byte(`{"text":"` + strings.Repeat("x", 100) + `"}`)
	rejection := request("POST", "/webhooks/expanded", expandedBody, "", sign(expandedBody, secret), "expanded", 400)
	if !bytes.Contains(rejection, []byte("mapped input exceeds")) {
		t.Fatalf("unexpected expansion rejection: %s", rejection)
	}
	runs, _ = s.ListRuns()
	if len(runs) != 0 {
		t.Fatal("oversized mapping created a run")
	}
	// Concurrent retries, including the first delivery, must share one run.
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("POST", "/webhooks/deploy", bytes.NewReader(valid))
			req.Header.Set("X-Event-Signature", sign(valid, secret))
			req.Header.Set("Idempotency-Key", "event-1")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != 201 && rec.Code != 200 {
				t.Errorf("concurrent delivery: %d %s", rec.Code, rec.Body.String())
			}
		}()
	}
	wg.Wait()
	runs, _ = s.ListRuns()
	if len(runs) != 1 {
		t.Fatalf("runs=%d", len(runs))
	}
	run, err := s.GetRun(runs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	pinned, _ := s.GetWorkflowVersion(digest)
	if run.PlanDigest != digest || !bytes.Equal(run.PlanJSON, pinned) || run.Input.(map[string]any)["count"] != float64(7) {
		t.Fatalf("pin/input mismatch: %+v", run)
	}
	// A new version never changes the trigger's pin.
	p.Name = "new-version"
	p.Digest = ""
	raw, _ = json.Marshal(p)
	request("POST", "/v1/workflow-versions", raw, "admin", "", "", 201)
	response := request("POST", "/webhooks/deploy", valid, "", sign(valid, secret), "event-1", 200)
	if !bytes.Contains(response, []byte(digest)) || !bytes.Contains(response, []byte(run.ID)) {
		t.Fatalf("duplicate changed pin: %s", response)
	}
	deliveries, err := s.ListDeliveries("deploy")
	if err != nil || len(deliveries) < 9 {
		t.Fatalf("missing audit history: %v %v", deliveries, err)
	}
	for _, d := range deliveries {
		if d.WorkflowDigest != digest {
			t.Fatal("delivery lost pin")
		}
	}
	// No idempotency key deliberately creates an independent run.
	request("POST", "/webhooks/deploy", valid, "", sign(valid, secret), "", 201)
	runs, _ = s.ListRuns()
	if len(runs) != 2 {
		t.Fatal("unkeyed delivery did not create run")
	}
}
