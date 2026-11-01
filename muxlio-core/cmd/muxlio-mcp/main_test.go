package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newTestServer creates an httptest.Server simulating the coordinator API.
func newTestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts
}

// mux returns a simple mux for the subset of coordinator endpoints we test.
func newCoordinatorMux() *http.ServeMux {
	mux := http.NewServeMux()

	// GET /healthz
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	// GET /v1/workers
	mux.HandleFunc("/v1/workers", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"workers":[],"registeredWorkers":[]}`))
	})

	// POST /v1/runs  |  GET /v1/runs
	mux.HandleFunc("/v1/runs", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"runId":"run_test123","state":"running"}`))
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"runs":[{"id":"run_test123","state":"running","workflowName":"test"}]}`))
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})

	// Sub-paths under /v1/runs/
	mux.HandleFunc("/v1/runs/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/v1/runs/")
		parts := strings.SplitN(path, "/", 3)
		runID := parts[0]

		switch {
		case len(parts) == 1 && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"` + runID + `","state":"succeeded"}`))

		case len(parts) == 2 && parts[1] == "graph":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"scopes":[],"activations":[]}`))

		case len(parts) == 2 && parts[1] == "activations":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"activations":[]}`))

		case len(parts) == 2 && parts[1] == "events":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"events":[],"after":0,"limit":50,"nextAfter":0,"hasMore":false}`))

		case len(parts) == 2 && parts[1] == "logs":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"logs":[]}`))

		case len(parts) == 2 && (parts[1] == "cancel" || parts[1] == "pause" || parts[1] == "resume"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"runId":"` + runID + `","action":"` + parts[1] + `"}`))

		case len(parts) == 3 && parts[1] == "signals":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"runId":"` + runID + `","signal":"` + parts[2] + `"}`))

		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"not_found","message":"not found"}`))
		}
	})

	return mux
}

// ---------------------------------------------------------------------------
// Local plan validation
// ---------------------------------------------------------------------------

func TestValidatePlanLocally_Valid(t *testing.T) {
	plan := json.RawMessage(`{
		"irVersion": "1.0",
		"name":      "test-plan",
		"subgraphs": {
			"root": {
				"nodes": [
					{"id": "step1", "kind": "execute", "executor": "agent/test"}
				],
				"output": {}
			}
		}
	}`)

	errs, err := validatePlanLocally(plan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("expected no validation errors, got: %v", errs)
	}
}

func TestValidatePlanLocally_MissingIRVersion(t *testing.T) {
	plan := json.RawMessage(`{
		"name": "test-plan",
		"subgraphs": {"root": {"nodes": [], "output": {}}}
	}`)
	errs, err := validatePlanLocally(plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) == 0 {
		t.Fatal("expected validation errors, got none")
	}
	found := false
	for _, e := range errs {
		if e["code"] == "MUXLIO_MISSING_IR_VERSION" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected MUXLIO_MISSING_IR_VERSION, got: %v", errs)
	}
}

func TestValidatePlanLocally_MissingName(t *testing.T) {
	plan := json.RawMessage(`{
		"irVersion": "1.0",
		"subgraphs": {"root": {"nodes": [], "output": {}}}
	}`)
	errs, _ := validatePlanLocally(plan)
	found := false
	for _, e := range errs {
		if e["code"] == "MUXLIO_MISSING_NAME" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected MUXLIO_MISSING_NAME in %v", errs)
	}
}

func TestValidatePlanLocally_MissingRootSubgraph(t *testing.T) {
	plan := json.RawMessage(`{
		"irVersion": "1.0",
		"name":      "test-plan",
		"subgraphs": {}
	}`)
	errs, _ := validatePlanLocally(plan)
	found := false
	for _, e := range errs {
		if e["code"] == "MUXLIO_MISSING_ROOT_SUBGRAPH" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected MUXLIO_MISSING_ROOT_SUBGRAPH in %v", errs)
	}
}

func TestValidatePlanLocally_UnsupportedIRVersion(t *testing.T) {
	plan := json.RawMessage(`{
		"irVersion": "9.0",
		"name":      "test-plan",
		"subgraphs": {"root": {"nodes": [], "output": {}}}
	}`)
	errs, _ := validatePlanLocally(plan)
	found := false
	for _, e := range errs {
		if e["code"] == "MUXLIO_UNSUPPORTED_IR_VERSION" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected MUXLIO_UNSUPPORTED_IR_VERSION in %v", errs)
	}
}

func TestValidatePlanLocally_DuplicateNodeID(t *testing.T) {
	plan := json.RawMessage(`{
		"irVersion": "1.0",
		"name":      "test-plan",
		"subgraphs": {
			"root": {
				"nodes": [
					{"id": "step1", "kind": "execute"},
					{"id": "step1", "kind": "execute"}
				]
			}
		}
	}`)
	errs, _ := validatePlanLocally(plan)
	found := false
	for _, e := range errs {
		if e["code"] == "MUXLIO_DUPLICATE_NODE_ID" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected MUXLIO_DUPLICATE_NODE_ID in %v", errs)
	}
}

func TestValidatePlanLocally_InvalidJSON(t *testing.T) {
	plan := json.RawMessage(`{not valid json}`)
	errs, _ := validatePlanLocally(plan)
	if len(errs) == 0 {
		t.Fatal("expected error for invalid JSON, got none")
	}
	if errs[0]["code"] != "MUXLIO_INVALID_JSON" {
		t.Fatalf("expected MUXLIO_INVALID_JSON, got: %v", errs[0]["code"])
	}
}

// ---------------------------------------------------------------------------
// Tool dispatch tests
// ---------------------------------------------------------------------------

func newTestToolServer(t *testing.T) *server {
	t.Helper()
	ts := newTestServer(t, newCoordinatorMux())
	return &server{api: newAPIClient(ts.URL)}
}

func TestToolCheckPlan_Valid(t *testing.T) {
	srv := newTestToolServer(t)
	args := json.RawMessage(`{"plan":{"irVersion":"1.0","name":"test","subgraphs":{"root":{"nodes":[]}}}}`)
	result := srv.dispatchTool("check_plan", args)
	if result.IsError {
		t.Fatalf("expected no error, got: %s", result.Content[0].Text)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(result.Content[0].Text), &body); err != nil {
		t.Fatal(err)
	}
	if body["valid"] != true {
		t.Fatalf("expected valid=true, got: %v", body)
	}
}

func TestToolCheckPlan_Invalid(t *testing.T) {
	srv := newTestToolServer(t)
	args := json.RawMessage(`{"plan":{"name":"test","subgraphs":{"root":{"nodes":[]}}}}`)
	result := srv.dispatchTool("check_plan", args)
	if !result.IsError {
		t.Fatal("expected isError=true for invalid plan")
	}
}

func TestToolCheckPlan_MissingPlan(t *testing.T) {
	srv := newTestToolServer(t)
	result := srv.dispatchTool("check_plan", json.RawMessage(`{}`))
	if !result.IsError {
		t.Fatal("expected error when plan is missing")
	}
}

func TestToolStartRun_Valid(t *testing.T) {
	srv := newTestToolServer(t)
	args := json.RawMessage(`{
		"plan":  {"irVersion":"1.0","name":"test","subgraphs":{"root":{"nodes":[]}}},
		"input": {}
	}`)
	result := srv.dispatchTool("start_run", args)
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(result.Content[0].Text), &body); err != nil {
		t.Fatal(err)
	}
	if body["runId"] != "run_test123" {
		t.Fatalf("expected runId run_test123, got: %v", body)
	}
}

func TestToolStartRun_InvalidPlan(t *testing.T) {
	srv := newTestToolServer(t)
	// Missing irVersion – local validation should reject before hitting coordinator.
	args := json.RawMessage(`{
		"plan":  {"name":"test","subgraphs":{"root":{"nodes":[]}}},
		"input": {}
	}`)
	result := srv.dispatchTool("start_run", args)
	if !result.IsError {
		t.Fatal("expected isError=true for invalid plan")
	}
	if !strings.Contains(result.Content[0].Text, "MUXLIO_PLAN_INVALID") {
		t.Fatalf("expected MUXLIO_PLAN_INVALID in error text, got: %s", result.Content[0].Text)
	}
}

func TestToolListRuns(t *testing.T) {
	srv := newTestToolServer(t)
	result := srv.dispatchTool("list_runs", json.RawMessage(`{}`))
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}
	if !strings.Contains(result.Content[0].Text, "runs") {
		t.Fatalf("expected runs field in response, got: %s", result.Content[0].Text)
	}
}

func TestToolGetRun(t *testing.T) {
	srv := newTestToolServer(t)
	result := srv.dispatchTool("get_run", json.RawMessage(`{"runId":"run_test123"}`))
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}
}

func TestToolGetRun_MissingRunID(t *testing.T) {
	srv := newTestToolServer(t)
	result := srv.dispatchTool("get_run", json.RawMessage(`{}`))
	if !result.IsError {
		t.Fatal("expected error for missing runId")
	}
}

func TestToolGetGraph(t *testing.T) {
	srv := newTestToolServer(t)
	result := srv.dispatchTool("get_graph", json.RawMessage(`{"runId":"run_test123"}`))
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}
}

func TestToolGetEvents_Defaults(t *testing.T) {
	srv := newTestToolServer(t)
	result := srv.dispatchTool("get_events", json.RawMessage(`{"runId":"run_test123"}`))
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}
}

func TestToolGetEvents_WithCursor(t *testing.T) {
	srv := newTestToolServer(t)
	after := 5
	argsBytes, _ := json.Marshal(map[string]any{
		"runId": "run_test123",
		"after": after,
		"limit": 10,
	})
	result := srv.dispatchTool("get_events", argsBytes)
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}
}

func TestToolGetEvents_LimitCapped(t *testing.T) {
	// Ensure limit > 200 is silently capped.
	srv := newTestToolServer(t)
	argsBytes, _ := json.Marshal(map[string]any{
		"runId": "run_test123",
		"limit": 9999,
	})
	result := srv.dispatchTool("get_events", argsBytes)
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}
}

func TestToolGetActivations(t *testing.T) {
	srv := newTestToolServer(t)
	result := srv.dispatchTool("get_activations", json.RawMessage(`{"runId":"run_test123"}`))
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}
}

func TestToolGetLogs(t *testing.T) {
	srv := newTestToolServer(t)
	result := srv.dispatchTool("get_logs", json.RawMessage(`{"runId":"run_test123"}`))
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}
}

func TestToolGetLogs_WithActivationID(t *testing.T) {
	srv := newTestToolServer(t)
	result := srv.dispatchTool("get_logs", json.RawMessage(`{"runId":"run_test123","activationId":"act_1"}`))
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}
}

func TestToolCancelRun(t *testing.T) {
	srv := newTestToolServer(t)
	result := srv.dispatchTool("cancel_run", json.RawMessage(`{"runId":"run_test123"}`))
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}
}

func TestToolPauseRun(t *testing.T) {
	srv := newTestToolServer(t)
	result := srv.dispatchTool("pause_run", json.RawMessage(`{"runId":"run_test123"}`))
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}
}

func TestToolResumeRun(t *testing.T) {
	srv := newTestToolServer(t)
	result := srv.dispatchTool("resume_run", json.RawMessage(`{"runId":"run_test123"}`))
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}
}

func TestToolSendSignal(t *testing.T) {
	srv := newTestToolServer(t)
	args := json.RawMessage(`{"runId":"run_test123","signalName":"approve","payload":{"approved":true}}`)
	result := srv.dispatchTool("send_signal", args)
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}
}

func TestToolSendSignal_MissingSignalName(t *testing.T) {
	srv := newTestToolServer(t)
	result := srv.dispatchTool("send_signal", json.RawMessage(`{"runId":"run_test123"}`))
	if !result.IsError {
		t.Fatal("expected error for missing signalName")
	}
}

func TestToolGetWorkers(t *testing.T) {
	srv := newTestToolServer(t)
	result := srv.dispatchTool("get_workers", json.RawMessage(`{}`))
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}
}

func TestToolServerHealth(t *testing.T) {
	srv := newTestToolServer(t)
	result := srv.dispatchTool("server_health", json.RawMessage(`{}`))
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}
	if !strings.Contains(result.Content[0].Text, "ok") {
		t.Fatalf("expected 'ok' in health response, got: %s", result.Content[0].Text)
	}
}

func TestToolServerHealth_Unreachable(t *testing.T) {
	// Point at a port that nothing is listening on.
	srv := &server{api: newAPIClient("http://127.0.0.1:1")}
	result := srv.dispatchTool("server_health", json.RawMessage(`{}`))
	if !result.IsError {
		t.Fatal("expected isError=true when coordinator is unreachable")
	}
}

func TestToolUnknown(t *testing.T) {
	srv := newTestToolServer(t)
	result := srv.dispatchTool("nonexistent_tool", json.RawMessage(`{}`))
	if !result.IsError {
		t.Fatal("expected error for unknown tool")
	}
}

// ---------------------------------------------------------------------------
// MCP protocol layer tests
// ---------------------------------------------------------------------------

func sendMCPMessage(t *testing.T, srv *mcpServer, msg any) jsonRPCResponse {
	t.Helper()
	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	srv.enc = json.NewEncoder(&buf)
	var req jsonRPCRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	srv.handle(req)
	var resp jsonRPCResponse
	if err := json.NewDecoder(&buf).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v (buf=%s)", err, buf.String())
	}
	return resp
}



func TestMCPInitialize(t *testing.T) {
	ts := newTestServer(t, newCoordinatorMux())
	var buf bytes.Buffer
	srv := &mcpServer{
		tools: &server{api: newAPIClient(ts.URL)},
		enc:   json.NewEncoder(&buf),
	}

	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
		Params:  json.RawMessage(`{}`),
	}
	srv.handle(req)

	var resp jsonRPCResponse
	if err := json.NewDecoder(&buf).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result type: %T", resp.Result)
	}
	if result["protocolVersion"] != "2024-11-05" {
		t.Fatalf("unexpected protocolVersion: %v", result["protocolVersion"])
	}
	info, _ := result["serverInfo"].(map[string]any)
	if info["name"] != "muxlio" {
		t.Fatalf("unexpected server name: %v", info["name"])
	}
}

func TestMCPToolsList(t *testing.T) {
	ts := newTestServer(t, newCoordinatorMux())
	var buf bytes.Buffer
	srv := &mcpServer{
		tools: &server{api: newAPIClient(ts.URL)},
		enc:   json.NewEncoder(&buf),
	}

	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`2`),
		Method:  "tools/list",
	}
	srv.handle(req)

	var resp jsonRPCResponse
	if err := json.NewDecoder(&buf).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result type: %T", resp.Result)
	}
	toolsList, ok := result["tools"].([]any)
	if !ok || len(toolsList) == 0 {
		t.Fatalf("expected non-empty tools list, got: %v", result)
	}
	// Verify expected tools are present.
	expectedTools := map[string]bool{
		"check_plan":    false,
		"start_run":     false,
		"list_runs":     false,
		"get_run":       false,
		"get_graph":     false,
		"get_events":    false,
		"get_activations": false,
		"get_logs":      false,
		"cancel_run":    false,
		"pause_run":     false,
		"resume_run":    false,
		"send_signal":   false,
		"get_workers":   false,
		"server_health": false,
	}
	for _, item := range toolsList {
		tool, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := tool["name"].(string)
		expectedTools[name] = true
	}
	for name, found := range expectedTools {
		if !found {
			t.Errorf("expected tool %q not found in tools/list", name)
		}
	}
}

func TestMCPToolsCall_CheckPlan(t *testing.T) {
	ts := newTestServer(t, newCoordinatorMux())
	var buf bytes.Buffer
	srv := &mcpServer{
		tools: &server{api: newAPIClient(ts.URL)},
		enc:   json.NewEncoder(&buf),
	}

	params, _ := json.Marshal(toolCallParams{
		Name: "check_plan",
		Arguments: json.RawMessage(`{"plan":{"irVersion":"1.0","name":"test","subgraphs":{"root":{"nodes":[]}}}}`),
	})
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`3`),
		Method:  "tools/call",
		Params:  params,
	}
	srv.handle(req)

	var resp jsonRPCResponse
	if err := json.NewDecoder(&buf).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected protocol error: %v", resp.Error)
	}
}

func TestMCPPing(t *testing.T) {
	ts := newTestServer(t, newCoordinatorMux())
	var buf bytes.Buffer
	srv := &mcpServer{
		tools: &server{api: newAPIClient(ts.URL)},
		enc:   json.NewEncoder(&buf),
	}
	req := jsonRPCRequest{JSONRPC: "2.0", ID: json.RawMessage(`4`), Method: "ping"}
	srv.handle(req)
	var resp jsonRPCResponse
	if err := json.NewDecoder(&buf).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("ping returned error: %v", resp.Error)
	}
}

func TestMCPMethodNotFound(t *testing.T) {
	ts := newTestServer(t, newCoordinatorMux())
	var buf bytes.Buffer
	srv := &mcpServer{
		tools: &server{api: newAPIClient(ts.URL)},
		enc:   json.NewEncoder(&buf),
	}
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`5`),
		Method:  "nonexistent/method",
	}
	srv.handle(req)
	var resp jsonRPCResponse
	if err := json.NewDecoder(&buf).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil {
		t.Fatal("expected error for unknown method")
	}
	if resp.Error.Code != -32601 {
		t.Fatalf("expected -32601 error code, got: %d", resp.Error.Code)
	}
}

func TestMCPNotification_NoResponse(t *testing.T) {
	// Notifications (no ID) must not produce a response.
	ts := newTestServer(t, newCoordinatorMux())
	var buf bytes.Buffer
	srv := &mcpServer{
		tools: &server{api: newAPIClient(ts.URL)},
		enc:   json.NewEncoder(&buf),
	}
	// notifications/initialized has no ID
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	}
	srv.handle(req)
	// buf should be empty (no response written)
	if buf.Len() != 0 {
		t.Fatalf("expected no response for notification, got: %s", buf.String())
	}
}

// ---------------------------------------------------------------------------
// apiClient URL validation
// ---------------------------------------------------------------------------

func TestAPIClientInvalidURL(t *testing.T) {
	// A bad URL should cause Do to fail cleanly.
	c := newAPIClient("http://127.0.0.1:1") // nothing listening
	_, err := c.get("/healthz", nil)
	if err == nil {
		t.Fatal("expected connection error for unreachable server")
	}
}

func TestAPIResultOrError_ErrorStatus(t *testing.T) {
	r := &apiResponse{statusCode: 404, body: []byte(`{"code":"not_found","message":"not found"}`)}
	result := apiResultOrError(r)
	if !result.IsError {
		t.Fatal("expected isError=true for 404 response")
	}
	if !strings.Contains(result.Content[0].Text, "404") {
		t.Fatalf("expected HTTP status in error text, got: %s", result.Content[0].Text)
	}
}

func TestAPIResultOrError_OKStatus(t *testing.T) {
	r := &apiResponse{statusCode: 200, body: []byte(`{"status":"ok"}`)}
	result := apiResultOrError(r)
	if result.IsError {
		t.Fatalf("unexpected isError=true for 200 response")
	}
}
