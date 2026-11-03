package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/artifacts"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
)

func TestObservabilityEndpoints(t *testing.T) {
	store := state.New()
	if err := store.CreateRun(&state.Run{ID: "run-api", WorkflowName: "demo"}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateActivation(&state.Activation{ID: "act-api", RunID: "run-api", NodeID: "task", ScopePath: []string{"map[0]"}, State: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendLogChunk(state.LogChunk{RunID: "run-api", ActivationID: "act-api", Stream: "stdout", Seq: 1, Data: []byte("hello"), Timestamp: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimAssignment(state.Assignment{RunID: "run-api", ActivationID: "act-api", WorkerID: "worker-1", LeaseUntil: time.Now().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	_ = store.AppendEvent("run-api", "custom", "subject", nil)
	handler := NewRouter(store, nil)
	for _, path := range []string{"/v1/runs/run-api/activations", "/v1/runs/run-api/graph", "/v1/runs/run-api/logs?activationId=act-api", "/v1/workers"} {
		req := httptest.NewRequest("GET", path, nil)
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		var body map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if resp.Code != 200 {
			t.Fatalf("GET %s: status %d body %v", path, resp.Code, body)
		}
		raw, _ := json.Marshal(body)
		if strings.Contains(string(raw), "NodeID") || strings.Contains(string(raw), "ActivationID") {
			t.Fatalf("non lower-camel JSON: %s", raw)
		}
		if strings.HasSuffix(path, "/graph") && body["edges"] != nil {
			t.Fatalf("graph should not invent authored edges: %v", body)
		}
		if path == "/v1/workers" {
			if _, ok := body["registeredWorkers"]; !ok {
				t.Fatalf("GET /v1/workers missing registeredWorkers: %v", body)
			}
		}
	}

	req := httptest.NewRequest("GET", "/v1/runs/run-api/events?after=0&limit=1", nil)
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	var page map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&page)
	if page["limit"] != float64(1) || page["hasMore"] != true || page["nextAfter"] != float64(1) {
		t.Fatalf("unexpected cursor metadata: %v", page)
	}

	req = httptest.NewRequest("GET", "/v1/runs/missing/graph", nil)
	resp = httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != 404 {
		t.Fatalf("missing run status = %d", resp.Code)
	}
	if err := store.RecordExecutionAttempt(state.ExecutionAttempt{ID: "attempt-api", RunID: "run-api", ActivationID: "act-api", RetryNumber: 1, Executor: "x", State: "succeeded", StartedAt: time.Now(), FinishedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	NewRouter(store, nil).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/runs/run-api/attempts?activationId=act-api", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "attempt-api") {
		t.Fatalf("attempt API response %d: %s", response.Code, response.Body.String())
	}
}

func TestAttemptsAPIExposesUsage(t *testing.T) {
	store := state.New()
	if err := store.CreateRun(&state.Run{ID: "run-usage"}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateActivation(&state.Activation{ID: "act-usage", RunID: "run-usage", NodeID: "n", State: "running"}); err != nil {
		t.Fatal(err)
	}
	// Attempt with reported usage.
	if err := store.RecordExecutionAttempt(state.ExecutionAttempt{ID: "att-1", RunID: "run-usage", ActivationID: "act-usage", RetryNumber: 1, Executor: "agent/x", State: "succeeded", StartedAt: time.Now(), FinishedAt: time.Now(), Usage: &state.TokenUsage{Input: 100, Output: 50, Cached: 25}}); err != nil {
		t.Fatal(err)
	}
	// Attempt with no usage (provider unavailable).
	if err := store.RecordExecutionAttempt(state.ExecutionAttempt{ID: "att-2", RunID: "run-usage", ActivationID: "act-usage", RetryNumber: 2, Executor: "agent/x", State: "succeeded", StartedAt: time.Now(), FinishedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	resp := httptest.NewRecorder()
	NewRouter(store, nil).ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/v1/runs/run-usage/attempts", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", resp.Code, resp.Body.String())
	}
	var page struct {
		Attempts []map[string]any `json:"attempts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if len(page.Attempts) != 2 {
		t.Fatalf("expected 2 attempts, got %d", len(page.Attempts))
	}

	// First attempt carries usage.
	att1 := page.Attempts[0]
	usage, ok := att1["usage"].(map[string]any)
	if !ok {
		t.Fatalf("expected usage object in attempt 1, got %v", att1)
	}
	if usage["input"] != float64(100) || usage["output"] != float64(50) || usage["cached"] != float64(25) {
		t.Fatalf("usage values mismatch: %v", usage)
	}

	// Second attempt has no usage key (missing = unavailable, not zero).
	if _, ok := page.Attempts[1]["usage"]; ok {
		t.Fatalf("missing usage should be omitted, not reported as zero: %v", page.Attempts[1])
	}
}

func TestHealthzMethods(t *testing.T) {
	handler := NewRouter(state.New(), nil)

	getResponse := httptest.NewRecorder()
	handler.ServeHTTP(getResponse, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if getResponse.Code != http.StatusOK {
		t.Fatalf("GET /healthz status = %d", getResponse.Code)
	}
	if got, want := getResponse.Body.String(), "{\"status\":\"ok\"}\n"; got != want {
		t.Fatalf("GET /healthz body = %q, want %q", got, want)
	}
	if got := getResponse.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("GET /healthz Content-Type = %q", got)
	}

	postResponse := httptest.NewRecorder()
	handler.ServeHTTP(postResponse, httptest.NewRequest(http.MethodPost, "/healthz", nil))
	if postResponse.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /healthz status = %d", postResponse.Code)
	}
	if got := postResponse.Header().Get("Allow"); got != http.MethodGet {
		t.Fatalf("POST /healthz Allow = %q, want %q", got, http.MethodGet)
	}
	var errBody map[string]any
	if err := json.NewDecoder(postResponse.Body).Decode(&errBody); err != nil {
		t.Fatalf("POST /healthz body decode: %v", err)
	}
	if errBody["code"] != CodeMethodNotAllowed {
		t.Fatalf("POST /healthz code = %q, want %q", errBody["code"], CodeMethodNotAllowed)
	}
	if errBody["fix"] == "" {
		t.Fatal("POST /healthz error envelope missing fix")
	}
}

func TestArtifactHTTPStreamingRoundTripAndDigestEnforcement(t *testing.T) {
	driver, err := artifacts.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	handler := NewRouterWithArtifacts(state.New(), nil, driver)
	body := "artifact bytes"
	sum := sha256.Sum256([]byte(body))
	digest := "sha256:" + hex.EncodeToString(sum[:])
	put := httptest.NewRecorder()
	handler.ServeHTTP(put, httptest.NewRequest(http.MethodPut, "/v1/artifacts/"+digest, strings.NewReader(body)))
	if put.Code != http.StatusCreated {
		t.Fatalf("PUT status = %d: %s", put.Code, put.Body.String())
	}
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/v1/artifacts/"+digest, nil))
	if get.Code != http.StatusOK || get.Body.String() != body || get.Header().Get("X-Artifact-SHA256") != digest {
		t.Fatalf("GET response = %d %q headers=%v", get.Code, get.Body.String(), get.Header())
	}
	bad := httptest.NewRecorder()
	handler.ServeHTTP(bad, httptest.NewRequest(http.MethodPut, "/v1/artifacts/sha256:"+strings.Repeat("0", 64), strings.NewReader(body)))
	if bad.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad digest PUT status = %d", bad.Code)
	}
	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/v1/artifacts/sha256:"+strings.Repeat("0", 64), nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing GET status = %d", missing.Code)
	}
	transfer := httptest.NewRecorder()
	handler.ServeHTTP(transfer, httptest.NewRequest(http.MethodGet, "/v1/artifacts/"+digest+"/transfer?method=GET&size=13", nil))
	if transfer.Code != http.StatusOK {
		t.Fatalf("transfer plan status = %d: %s", transfer.Code, transfer.Body.String())
	}
	var plan map[string]any
	if err := json.Unmarshal(transfer.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan["mode"] != "proxy" {
		t.Fatalf("expected proxy mode, got %v", plan["mode"])
	}
}

type workerBackendFixture struct {
	registered  bool
	heartbeat   bool
	completed   bool
	registerErr error
}

func (f *workerBackendFixture) Register(r WorkerRegistration) (WorkerRegistrationResult, error) {
	f.registered = true
	if f.registerErr != nil {
		return WorkerRegistrationResult{}, f.registerErr
	}
	return WorkerRegistrationResult{CoordinatorEpoch: 3, HeartbeatSeconds: 15}, nil
}
func (f *workerBackendFixture) Heartbeat(string, WorkerHeartbeat) (WorkerHeartbeatResult, error) {
	f.heartbeat = true
	return WorkerHeartbeatResult{CoordinatorEpoch: 3, LeaseSeconds: 45}, nil
}

type workerProtocolTestError struct{}

func (workerProtocolTestError) Error() string { return "worker is not registered" }
func (workerProtocolTestError) ProtocolError() (int, string, string) {
	return http.StatusConflict, "worker_not_registered", "worker must register first"
}
func (f *workerBackendFixture) NextAssignment(string) (*WorkerAssignment, error) {
	return &WorkerAssignment{AssignmentID: "as-1", RunID: "run-1", ActivationID: "act-1", AttemptID: "attempt-1", CoordinatorEpoch: 3, Generation: 2, LeaseUntil: "2030-01-01T00:00:00Z", Request: json.RawMessage(`{"executor":"test"}`)}, nil
}
func (f *workerBackendFixture) Complete(string, WorkerCompletion) error {
	f.completed = true
	return nil
}
func (f *workerBackendFixture) AppendWorkerLog(string, WorkerLogAppend) error { return nil }

func TestWorkerProtocolLifecycleMessages(t *testing.T) {
	backend := &workerBackendFixture{}
	protocol := NewWorkerProtocolHandler(backend)
	handler := NewRouter(state.New(), nil, protocol)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		return resp
	}
	reg := request(http.MethodPost, "/v1/workers/register", `{"protocolVersion":"v1","workerId":"worker-a","capacity":2,"capabilities":[{"name":"agent/test","version":"1"}]}`)
	if reg.Code != http.StatusOK || !backend.registered {
		t.Fatalf("registration response %d: %s", reg.Code, reg.Body.String())
	}
	hb := request(http.MethodPost, "/v1/workers/worker-a/heartbeat", `{"protocolVersion":"v1","activeAssignments":[]}`)
	if hb.Code != http.StatusOK || !backend.heartbeat {
		t.Fatalf("heartbeat response %d: %s", hb.Code, hb.Body.String())
	}
	if got := request(http.MethodGet, "/v1/workers/worker-a/assignments", "").Code; got != http.StatusOK {
		t.Fatalf("assignment status = %d", got)
	}
	complete := request(http.MethodPost, "/v1/workers/worker-a/assignments/complete", `{"assignmentId":"as-1","runId":"r","activationId":"a","attemptId":"at","coordinatorEpoch":3,"generation":2,"state":"succeeded","outputs":{"value":"ok"}}`)
	if complete.Code != http.StatusAccepted || !backend.completed {
		t.Fatalf("completion response %d: %s", complete.Code, complete.Body.String())
	}
	wrongMethod := request(http.MethodGet, "/v1/workers/register", "")
	if wrongMethod.Code != http.StatusMethodNotAllowed || wrongMethod.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("registration method response = %d, Allow %q", wrongMethod.Code, wrongMethod.Header().Get("Allow"))
	}
	wrongAssignmentMethod := request(http.MethodPost, "/v1/workers/worker-a/assignments", `{}`)
	if wrongAssignmentMethod.Code != http.StatusMethodNotAllowed || wrongAssignmentMethod.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("assignment method response = %d, Allow %q", wrongAssignmentMethod.Code, wrongAssignmentMethod.Header().Get("Allow"))
	}
	unsupported := request(http.MethodPost, "/v1/workers/register", `{"protocolVersion":"v2","workerId":"worker-a","capacity":1}`)
	if unsupported.Code != http.StatusBadRequest || !strings.Contains(unsupported.Body.String(), `"code":"`+CodeUnsupportedProtocol+`"`) {
		t.Fatalf("unsupported protocol response = %d: %s", unsupported.Code, unsupported.Body.String())
	}
	duplicate := request(http.MethodPost, "/v1/workers/register", `{"protocolVersion":"v1","workerId":"worker-a","capacity":1,"capabilities":[{"name":"agent/test"},{"name":" agent/test "}]}`)
	if duplicate.Code != http.StatusBadRequest {
		t.Fatalf("duplicate capability status = %d", duplicate.Code)
	}
}

func TestGetWorkersListsLiveRegisteredWorkers(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "workers-api.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.CreateRun(&state.Run{ID: "run-workers", WorkflowName: "demo"}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateActivation(&state.Activation{ID: "act-workers", RunID: "run-workers", NodeID: "task", State: "running"}); err != nil {
		t.Fatal(err)
	}
	coordinator := NewCoordinator(store)
	protocol := NewWorkerProtocolHandler(coordinator)
	handler := NewRouter(store, nil, protocol)
	if _, err := coordinator.Register(WorkerRegistration{WorkerID: "worker-live", Capacity: 2, Capabilities: []WorkerCapability{{Name: "agent/test"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Register(WorkerRegistration{WorkerID: "worker-idle", Capacity: 1, Capabilities: []WorkerCapability{{Name: "agent/other"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Heartbeat("worker-live", WorkerHeartbeat{Active: []string{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Heartbeat("worker-idle", WorkerHeartbeat{Active: []string{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimAssignment(state.Assignment{RunID: "run-workers", ActivationID: "act-workers", WorkerID: "worker-live", LeaseUntil: time.Now().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterWorker(state.Worker{ID: "worker-stale", Capacity: 1, Capabilities: []state.WorkerCapability{{Name: "agent/test"}}, LastSeen: time.Now().Add(-2 * WorkerHeartbeatWindow)}); err != nil {
		t.Fatal(err)
	}
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/v1/workers", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("GET /v1/workers status = %d: %s", resp.Code, resp.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var assignments []state.Assignment
	if err := json.Unmarshal(body["workers"], &assignments); err != nil {
		t.Fatal(err)
	}
	if len(assignments) != 1 || assignments[0].WorkerID != "worker-live" || assignments[0].State != "active" {
		t.Fatalf("assignments = %#v", assignments)
	}
	var registered []state.Worker
	if err := json.Unmarshal(body["registeredWorkers"], &registered); err != nil {
		t.Fatal(err)
	}
	if len(registered) != 2 {
		t.Fatalf("registeredWorkers = %#v", registered)
	}
	seen := map[string]bool{}
	for _, w := range registered {
		seen[w.ID] = true
	}
	if !seen["worker-live"] || !seen["worker-idle"] {
		t.Fatalf("registeredWorkers = %#v", registered)
	}
}

func TestGetWorkersRegisteredWorkersEmptyArrayNotNull(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "workers-empty.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler := NewRouter(store, nil, nil)
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/v1/workers", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("GET /v1/workers status = %d: %s", resp.Code, resp.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if string(body["registeredWorkers"]) != "[]" {
		t.Fatalf("registeredWorkers should be [], got %s", body["registeredWorkers"])
	}
}

func TestCoordinatorAppendWorkerLogRequiresActiveAssignment(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "worker-log.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.CreateRun(&state.Run{ID: "run-log", WorkflowName: "demo"}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateActivation(&state.Activation{ID: "act-log", RunID: "run-log", NodeID: "task", State: "running"}); err != nil {
		t.Fatal(err)
	}
	c := NewCoordinator(store)
	if _, err := c.Register(WorkerRegistration{WorkerID: "worker-log", Capacity: 1, Capabilities: []WorkerCapability{{Name: "agent/test"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimAssignment(state.Assignment{RunID: "run-log", ActivationID: "act-log", WorkerID: "worker-log", AssignmentID: "as-log", AttemptID: "attempt-log", LeaseUntil: time.Now().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	assignments, _ := store.ListAssignments("run-log")
	var generation int64
	for _, a := range assignments {
		if a.AssignmentID == "as-log" {
			generation = a.Generation
		}
	}
	if generation < 1 {
		t.Fatal("missing assignment generation")
	}
	err = c.AppendWorkerLog("worker-log", WorkerLogAppend{
		AssignmentID: "as-log", RunID: "run-log", ActivationID: "act-log", AttemptID: "attempt-log",
		CoordinatorEpoch: c.epoch, Generation: generation, Stream: "agent", Seq: 1, Data: []byte("live"),
	})
	if err != nil {
		t.Fatal(err)
	}
	logs, err := store.GetLogChunks("run-log", "act-log")
	if err != nil || len(logs) != 1 || string(logs[0].Data) != "live" {
		t.Fatalf("logs=%+v err=%v", logs, err)
	}
}

func TestWorkerProtocolPreservesWrappedBackendProtocolErrors(t *testing.T) {
	backend := &workerBackendFixture{registerErr: fmt.Errorf("registration rejected: %w", workerProtocolTestError{})}
	protocol := NewWorkerProtocolHandler(backend)
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/workers/register", strings.NewReader(`{"protocolVersion":"v1","workerId":"worker-a","capacity":1,"capabilities":[]}`))
	protocol.ServeHTTP(resp, req)
	if resp.Code != http.StatusConflict || !strings.Contains(resp.Body.String(), `"code":"worker_not_registered"`) {
		t.Fatalf("wrapped protocol error response = %d: %s", resp.Code, resp.Body.String())
	}
}
