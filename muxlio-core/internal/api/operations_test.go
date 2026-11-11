package api

import (
	"bytes"
	"encoding/json"
	"github.com/muxlio/muxlio-core/internal/testaccounting"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/artifacts"
	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/schedule"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/subscription"
	"github.com/muxlio/muxlio-core/internal/trigger"
)

func TestOperationalAPIsPersistEvidenceAndProtectScopes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.db")
	s, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	local, err := artifacts.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CreateRun(&state.Run{ID: "run"}); err != nil {
		t.Fatal(err)
	}
	if err = s.EnqueueDispatch(state.Dispatch{AssignmentID: "queued", ActivationID: "queued", AttemptID: "queued", RunID: "run", Executor: "generic/echo", Request: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"held", "detached"} {
		st := []string{"cancelled", "detached"}[i]
		if err = s.RecordExecutionAttempt(state.ExecutionAttempt{ID: id, RunID: "run", ActivationID: "activation", State: st, StartedAt: time.Now(), AwaitReconciliationReason: map[bool]string{true: "Verify external outcome", false: ""}[i == 0]}); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.RecordExecutionAttempt(state.ExecutionAttempt{ID: "ordinary", RunID: "run", ActivationID: "aaa", State: "succeeded", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	router := WithAuth(NewRouterWithArtifacts(s, nil, local), "admin", "worker", AccessGrant{Token: "scoped", RunID: "run", Permissions: []string{"read", "control"}})
	request := func(method, path string, body any, token string, status int) []byte {
		t.Helper()
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.RemoteAddr = "192.0.2.2:1234"
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: %d want %d: %s", method, path, w.Code, status, w.Body.String())
		}
		return w.Body.Bytes()
	}
	for _, path := range []string{"/v1/operations", "/v1/retention", "/v1/reports", "/v1/revisions?runId=run", "/v1/automation/history?kind=deadletters"} {
		request("GET", path, nil, "", 401)
		request("GET", path, nil, "worker", 403)
		request("GET", path, nil, "scoped", 403)
	}
	var ops struct {
		Items   []state.OperationalItem
		Counts  map[string]int
		HasMore bool
	}
	if err = json.Unmarshal(request("GET", "/v1/operations?kind=attention&limit=1", nil, "admin", 200), &ops); err != nil {
		t.Fatal(err)
	}
	if len(ops.Items) != 1 || ops.Items[0].AttemptID != "held" || ops.Counts["detached"] != 1 {
		t.Fatalf("unexpected operations %+v", ops)
	}
	if ops.Counts["queue"] != 1 {
		t.Fatal("queued work is missing from operational counts")
	}
	var attempts struct{ Attempts []state.ExecutionAttempt }
	if err = json.Unmarshal(request("GET", "/v1/runs/run/attempts?limit=1&details=false", nil, "admin", 200), &attempts); err != nil || len(attempts.Attempts) != 1 || attempts.Attempts[0].ID != "held" {
		t.Fatalf("held attempt must appear on the first page: %+v %v", attempts, err)
	}
	const deliverySecret = "do-not-render-delivery-payload"
	for i, id := range []string{"old", "middle", "latest"} {
		if err = s.EnqueueDelivery(subscription.Delivery{ID: id, SubscriptionID: "notify", RunID: "run", EventType: "run.failed", State: subscription.StateDeadLetter, CreatedAt: time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC), Payload: map[string]any{"value": deliverySecret}}); err != nil {
			t.Fatal(err)
		}
	}
	var history struct {
		Records []subscription.Delivery
		HasMore bool
	}
	historyRaw := request("GET", "/v1/automation/history?kind=deadletters&limit=2", nil, "admin", 200)
	if bytes.Contains(historyRaw, []byte(deliverySecret)) || bytes.Contains(historyRaw, []byte(`"payload"`)) {
		t.Fatal("delivery payload leaked into operational history")
	}
	if err = json.Unmarshal(historyRaw, &history); err != nil || len(history.Records) != 2 || !history.HasMore || history.Records[0].ID != "latest" {
		t.Fatalf("bad delivery history: %+v %v", history, err)
	}
	_ = json.Unmarshal(request("GET", "/v1/automation/history?kind=deadletters&offset=2&limit=2", nil, "admin", 200), &history)
	if len(history.Records) != 1 || history.Records[0].ID != "old" || history.HasMore {
		t.Fatal("delivery history cannot reach older records")
	}
	request("POST", "/v1/retention", map[string]any{"kind": "run", "id": "run", "pinned": true}, "admin", 200)
	if run, _ := s.GetRun("run"); !run.Pinned {
		t.Fatal("run pin not exposed")
	}
	deadline := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	request("POST", "/v1/retention", map[string]any{"kind": "run", "id": "run", "retainedUntil": deadline}, "admin", 200)
	if run, _ := s.GetRun("run"); run.RetainedUntil == nil || !run.RetainedUntil.Equal(deadline) {
		t.Fatal("retention deadline lost")
	}
	request("POST", "/v1/retention", map[string]any{"kind": "artifact", "id": "sha256:" + strings.Repeat("0", 64), "pinned": true}, "admin", 404)
	ref, err := local.Put([]byte("snapshot"), "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	request("POST", "/v1/retention", map[string]any{"kind": "artifact", "id": ref.Digest, "pinned": true}, "admin", 200)
	for i := 0; i < 3; i++ {
		if err = s.AppendEvent("run", "artifact.snapshot", "attempt", map[string]any{"activationId": "activation", "snapshot": map[string]any{"digest": ref.Digest, "name": "report"}}); err != nil {
			t.Fatal(err)
		}
	}
	var revisions struct {
		Revisions  []state.Event
		NextBefore int
		HasMore    bool
	}
	_ = json.Unmarshal(request("GET", "/v1/revisions?runId=run&limit=2", nil, "admin", 200), &revisions)
	if len(revisions.Revisions) != 2 || !revisions.HasMore || revisions.Revisions[0].Seq <= revisions.Revisions[1].Seq {
		t.Fatal("revision history is not bounded newest first")
	}
	summary, err := state.SummarizeAccounting([]state.ExecutionAttempt{{State: "succeeded", Accounting: testaccounting.Tokens(0, 0, 0)}})
	if err != nil {
		t.Fatal(err)
	}
	document, err := json.Marshal(map[string]any{"manifest": map[string]any{"datasetId": "cases", "versions": []any{map[string]any{"id": "v1", "planDigest": "sha256:plan"}}}, "results": []any{map[string]any{"caseId": "zero", "versionId": "v1", "durationMs": 0, "execution": map[string]any{"state": "succeeded", "success": true, "accounting": summary}}}})
	if err != nil {
		t.Fatal(err)
	}
	doc := json.RawMessage(document)
	body := map[string]any{"kind": "evaluation", "name": "Evidence", "document": doc}
	var imported state.OperationalReport
	_ = json.Unmarshal(request("POST", "/v1/reports", body, "admin", 201), &imported)
	if imported.ID == "" || imported.Document != nil {
		t.Fatal("import response should contain immutable identity, not payload")
	}
	_ = json.Unmarshal(request("GET", "/v1/reports/"+imported.ID, nil, "admin", 200), &imported)
	if !bytes.Contains(imported.Document, []byte(`"knownSubtotal":"0"`)) || !bytes.Contains(imported.Document, []byte(`"knownSubtotal":null`)) {
		t.Fatal("availability was altered")
	}
	request("POST", "/v1/reports", map[string]any{"kind": "evaluation", "name": "bad", "document": map[string]any{"results": []any{}}}, "admin", 400)
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	restored, err := s.GetOperationalReport(imported.ID)
	if err != nil || !bytes.Equal(restored.Document, imported.Document) {
		t.Fatal("report evidence did not survive restart")
	}
}

func TestAutomationSwitchesPreserveDefinitionsAndSecrets(t *testing.T) {
	s, err := sqlite.Open(filepath.Join(t.TempDir(), "automation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := &plan.Plan{Name: "test", IRVersion: "1.0", Root: "root", InputSchema: json.RawMessage(`{"type":"integer"}`), Subgraphs: map[string]plan.Subgraph{"root": {Nodes: []plan.Node{{ID: "task", Kind: plan.KindExecute, Executor: "generic/echo"}}}}}
	digest, err := p.ComputeDigest()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(p)
	if err = s.SaveWorkflowVersion(digest, raw); err != nil {
		t.Fatal(err)
	}
	d := schedule.Definition{ID: "clock", WorkflowDigest: digest, Input: json.RawMessage(`0`), Kind: "once", StartAt: time.Now().UTC().Add(-time.Minute), Overlap: "skip", MaxCatchUp: 1}
	if err = s.CreateSchedule(d); err != nil {
		t.Fatal(err)
	}
	tr := trigger.Trigger{ID: "webhook", WorkflowDigest: digest, Secret: "0123456789abcdef", Enabled: true}
	if err = tr.Normalize(); err != nil {
		t.Fatal(err)
	}
	if err = s.CreateTrigger(tr); err != nil {
		t.Fatal(err)
	}
	sub := subscription.Subscription{ID: "notify", Target: "https://example.invalid/events", Secret: "0123456789abcdef", Enabled: true, EventTypes: []string{"run.*"}}
	if err = s.CreateSubscription(sub); err != nil {
		t.Fatal(err)
	}
	h := NewRouter(s, nil)
	for _, pair := range [][2]string{{"schedules", "clock"}, {"triggers", "webhook"}, {"subscriptions", "notify"}} {
		raw, _ = json.Marshal(map[string]any{"kind": pair[0], "id": pair[1], "enabled": false})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/automation", bytes.NewReader(raw)))
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
	if ids, err := s.TickSchedules(time.Now().UTC()); err != nil || len(ids) != 0 {
		t.Fatalf("paused schedule fired: %v %v", ids, err)
	}
	listed, err := s.ListSchedules()
	if err != nil || len(listed) != 1 || !listed[0].Paused {
		t.Fatal("pause not visible")
	}
	gotTr, _ := s.GetTrigger("webhook")
	gotSub, _ := s.GetSubscription("notify")
	if gotTr.Enabled || gotSub.Enabled || gotTr.Secret != tr.Secret || gotSub.Secret != sub.Secret {
		t.Fatal("toggle changed credentials or did not disable")
	}
	if err = s.SetAutomationEnabled("schedules", "clock", true); err != nil {
		t.Fatal(err)
	}
	if ids, err := s.TickSchedules(time.Now().UTC()); err != nil || len(ids) != 1 {
		t.Fatalf("resumed schedule did not fire: %v %v", ids, err)
	}
}
