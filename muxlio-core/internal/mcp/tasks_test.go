package mcp

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/muxlio/muxlio-core/internal/api"
	"github.com/muxlio/muxlio-core/internal/plan"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
)

type taskRunner struct {
	store state.Store
	fail  bool
}

func (r *taskRunner) CreateRun(json.RawMessage) (string, error) {
	return "", errors.New("plan required")
}
func (r *taskRunner) CreatePlanRun(p *plan.Plan, input json.RawMessage) (string, error) {
	if r.fail {
		return "", errors.New("persistence failed")
	}
	return plan.NewEngineWithStore(p, nil, r.store).CreateRun(input)
}
func (r *taskRunner) Run(string) error                   { return nil }
func (r *taskRunner) ControlRun(id, action string) error { return r.store.SetRunState(id, "cancelled") }

func taskMeta() map[string]any {
	return map[string]any{
		"io.modelcontextprotocol/protocolVersion":    tasksProtocolVersion,
		"io.modelcontextprotocol/clientInfo":         serverInfo{Name: "test", Version: "1"},
		"io.modelcontextprotocol/clientCapabilities": map[string]any{"extensions": map[string]any{tasksExtension: map[string]any{}}, "elicitation": map[string]any{"form": map[string]any{}}},
	}
}
func taskRPC(t *testing.T, s *server, method string, params map[string]any) (map[string]any, *jsonRPCError) {
	t.Helper()
	raw, _ := json.Marshal(params)
	var out bytes.Buffer
	rpc := &mcpServer{tools: s, enc: json.NewEncoder(&out)}
	rpc.handle(jsonRPCRequest{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: method, Params: raw})
	var resp struct {
		Result map[string]any `json:"result"`
		Error  *jsonRPCError  `json:"error"`
	}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatal(err, out.String())
	}
	return resp.Result, resp.Error
}
func taskParams(handle string) map[string]any {
	return map[string]any{"taskId": handle, "_meta": taskMeta()}
}
func taskStartParams(name string) map[string]any {
	args := map[string]any{"plan": json.RawMessage(`{"irVersion":"1.0","name":"tasks","subgraphs":{"root":{"nodes":[{"id":"work","kind":"execute","executor":"test"}],"output":{}}}}`), "input": map[string]any{}}
	return map[string]any{"name": name, "arguments": args, "_meta": taskMeta()}
}

func TestTasksDurableCreationAndLifecycle(t *testing.T) {
	db := filepath.Join(t.TempDir(), "tasks.db")
	store, err := sqlite.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	runner := &taskRunner{store: store}
	upstream := newTestServer(t, api.WithAuth(api.NewRouter(store, runner), "admin", "worker"))
	s := &server{api: newAPIClient(upstream.URL, "admin")}
	for _, name := range []string{"start_run"} {
		created, rpcErr := taskRPC(t, s, "tools/call", taskStartParams(name))
		if rpcErr != nil || created["resultType"] != "task" || created["status"] != "working" {
			t.Fatalf("create: %v %v", created, rpcErr)
		}
		handle := created["taskId"].(string)
		got, rpcErr := taskRPC(t, s, "tasks/get", taskParams(handle))
		if rpcErr != nil || got["taskId"] != handle || got["resultType"] != "complete" {
			t.Fatalf("immediate get: %v %v", got, rpcErr)
		}
		// Reopen the database and reconstruct both API and MCP layers; no in-memory
		// task registry may be necessary to resolve the returned handle.
		upstream.Close()
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		store, err = sqlite.Open(db)
		if err != nil {
			t.Fatal(err)
		}
		runner.store = store
		upstream = newTestServer(t, api.WithAuth(api.NewRouter(store, runner), "admin", "worker"))
		s = &server{api: newAPIClient(upstream.URL, "admin")}
		got, rpcErr = taskRPC(t, s, "tasks/get", taskParams(handle))
		if rpcErr != nil || got["taskId"] != handle {
			t.Fatalf("recovered get: %v %v", got, rpcErr)
		}
		id, _ := taskRunID(handle)
		wantError := name == "start_run"
		if wantError {
			if err := store.SetRunError(id, errors.New("execution failed")); err != nil {
				t.Fatal(err)
			}
		} else if err := store.SetRunState(id, "succeeded"); err != nil {
			t.Fatal(err)
		}
		got, rpcErr = taskRPC(t, s, "tasks/get", taskParams(handle))
		if rpcErr != nil || got["status"] != "completed" || got["result"].(map[string]any)["isError"] != wantError {
			t.Fatalf("tool failure: %v %v", got, rpcErr)
		}
	}
	runner.fail = true
	got, rpcErr := taskRPC(t, s, "tools/call", taskStartParams("start_run"))
	if rpcErr != nil || got["resultType"] != "complete" || got["isError"] != true || got["taskId"] != nil {
		t.Fatalf("failed persistence: %v %v", got, rpcErr)
	}
}

func TestTasksInputAuthorizationAndCancellation(t *testing.T) {
	store := state.New()
	if err := store.CreateRun(&state.Run{ID: "run_inputs"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		if err := store.CreateInputRequest(&state.InputRequest{RunID: "run_inputs", RequestID: id, Name: "Review", Schema: json.RawMessage(`{"type":"boolean"}`), Permissions: []string{"review"}}); err != nil {
			t.Fatal(err)
		}
	}
	runner := &taskRunner{store: store}
	upstream := newTestServer(t, api.WithAuth(api.NewRouter(store, runner), "admin", "worker",
		api.AccessGrant{Token: "allowed", RunID: "run_inputs", Permissions: []string{"read", "inputs", "review", "control"}},
		api.AccessGrant{Token: "denied", RunID: "run_inputs", Permissions: []string{"read", "inputs"}},
		api.AccessGrant{Token: "other", RunID: "another", Permissions: []string{"read", "inputs", "review", "control"}},
		api.AccessGrant{Token: "read-only", RunID: "run_inputs", Permissions: []string{"read"}},
	))
	s := func(token string) *server { return &server{api: newAPIClient(upstream.URL, token)} }
	handle := taskHandle("run_inputs")
	got, rpcErr := taskRPC(t, s("allowed"), "tasks/get", taskParams(handle))
	if rpcErr != nil || got["status"] != "input_required" || len(got["inputRequests"].(map[string]any)) != 2 {
		t.Fatalf("input: %v %v", got, rpcErr)
	}
	request := got["inputRequests"].(map[string]any)["a"].(map[string]any)
	if request["method"] != "elicitation/create" {
		t.Fatal(request)
	}
	update := taskParams(handle)
	update["inputResponses"] = map[string]any{"a": map[string]any{"action": "accept", "content": map[string]any{"json": "false"}}, "unknown": map[string]any{"action": "accept", "content": map[string]any{"json": "true"}}}
	for _, token := range []string{"denied", "other", "read-only", "worker", "invalid"} {
		if _, rpcErr := taskRPC(t, s(token), "tasks/update", update); rpcErr == nil {
			t.Fatalf("%s bypassed input authorization", token)
		}
		req, _ := store.GetInputRequest("run_inputs", "a")
		if req.State != state.InputRequestPending {
			t.Fatal("unauthorized input was accepted")
		}
	}
	got, rpcErr = taskRPC(t, s("allowed"), "tasks/update", update)
	if rpcErr != nil || len(got) != 1 || got["resultType"] != "complete" {
		t.Fatalf("update ack: %v %v", got, rpcErr)
	}
	req, _ := store.GetInputRequest("run_inputs", "a")
	if req.State != state.InputRequestFulfilled || req.Response != false || req.ResponderID == "" {
		t.Fatalf("response: %+v", req)
	}
	// Replayed/unknown keys must be ignored, while another request remains pending.
	if _, rpcErr := taskRPC(t, s("allowed"), "tasks/update", update); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	got, rpcErr = taskRPC(t, s("allowed"), "tasks/get", taskParams(handle))
	if rpcErr != nil || len(got["inputRequests"].(map[string]any)) != 1 {
		t.Fatalf("partial: %v %v", got, rpcErr)
	}
	update["inputResponses"] = map[string]any{"b": map[string]any{"action": "accept", "content": map[string]any{"json": `"wrong type"`}}}
	if _, rpcErr := taskRPC(t, s("allowed"), "tasks/update", update); rpcErr == nil {
		t.Fatal("schema validation bypassed")
	}
	for _, method := range []string{"tasks/get", "tasks/cancel", "tasks/update"} {
		if _, rpcErr := taskRPC(t, s("other"), method, update); rpcErr == nil {
			t.Fatalf("cross-run %s authorized", method)
		}
	}
	if _, rpcErr := taskRPC(t, s("read-only"), "tasks/cancel", taskParams(handle)); rpcErr == nil {
		t.Fatal("control permission bypassed")
	}
	got, rpcErr = taskRPC(t, s("allowed"), "tasks/cancel", taskParams(handle))
	if rpcErr != nil || len(got) != 1 {
		t.Fatalf("cancel ack: %v %v", got, rpcErr)
	}
	got, rpcErr = taskRPC(t, s("allowed"), "tasks/get", taskParams(handle))
	if rpcErr != nil || got["status"] != "cancelled" {
		t.Fatalf("cancelled: %v %v", got, rpcErr)
	}
	if _, rpcErr := taskRPC(t, s("allowed"), "tasks/get", taskParams(taskHandle("missing"))); rpcErr == nil {
		t.Fatal("unknown task accepted")
	}
}

func TestTasksNegotiationAndLegacyFallback(t *testing.T) {
	store := state.New()
	upstream := newTestServer(t, api.WithAuth(api.NewRouter(store, &taskRunner{store: store}), "admin", "worker"))
	s := &server{api: newAPIClient(upstream.URL, "admin")}
	for _, name := range []string{"start_run"} {
		for _, mode := range []string{"legacy", "experimental", "modern-no-extension"} {
			params := taskStartParams(name)
			if mode == "modern-no-extension" {
				params["_meta"].(map[string]any)["io.modelcontextprotocol/clientCapabilities"] = map[string]any{}
			} else {
				delete(params, "_meta")
				if mode == "experimental" {
					params["task"] = map[string]any{"ttl": 60000}
				}
			}
			got, err := taskRPC(t, s, "tools/call", params)
			if err != nil || got["taskId"] != nil || got["content"] == nil || got["isError"] == true {
				t.Fatalf("%s %s: %v %v", name, mode, got, err)
			}
		}
	}
	got, err := taskRPC(t, s, "server/discover", map[string]any{"_meta": taskMeta()})
	if err != nil || got["capabilities"].(map[string]any)["extensions"].(map[string]any)[tasksExtension] == nil {
		t.Fatalf("discovery: %v %v", got, err)
	}
	params := taskParams(taskHandle("missing"))
	params["_meta"].(map[string]any)["io.modelcontextprotocol/clientCapabilities"] = map[string]any{"tasks": map[string]any{}}
	if _, err := taskRPC(t, s, "tasks/get", params); err == nil || err.Code != -32021 {
		t.Fatalf("experimental capability accepted: %v", err)
	}
	for _, handle := range []string{"definition", "mcp-run:", taskHandle("../runs"), "mcp-run:%%%"} {
		if _, err := taskRPC(t, s, "tasks/get", taskParams(handle)); err == nil || err.Code != -32602 {
			t.Fatalf("invalid handle %s: %v", handle, err)
		}
	}
	for _, method := range []string{"tasks/list", "tasks/result"} {
		if _, err := taskRPC(t, s, method, params); err == nil || err.Code != -32601 {
			t.Fatalf("legacy method %s: %v", method, err)
		}
	}
}

func TestTasksHTTPModernRequests(t *testing.T) {
	store := state.New()
	upstream := newTestServer(t, api.WithAuth(api.NewRouter(store, &taskRunner{store: store}), "admin", "worker"))
	handler, err := newHTTPHandler(&server{api: newAPIClient(upstream.URL, "admin")}, "listener-token", nil)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method string, params map[string]any, headers map[string]string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		return httpMCPRequest(handler, http.MethodPost, string(body), headers)
	}
	headers := map[string]string{"MCP-Protocol-Version": tasksProtocolVersion, "Mcp-Method": "tools/call", "Mcp-Name": "start_run"}
	w := call("tools/call", taskStartParams("start_run"), headers)
	var created struct {
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || w.Header().Get("MCP-Protocol-Version") != tasksProtocolVersion || created.Result["resultType"] != "task" {
		t.Fatalf("HTTP create: %d %s", w.Code, w.Body)
	}
	handle := created.Result["taskId"].(string)
	headers["Mcp-Name"] = handle
	headers["Mcp-Method"] = "tasks/get"
	w = call("tasks/get", taskParams(handle), headers)
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"status":"working"`)) {
		t.Fatalf("HTTP get: %d %s", w.Code, w.Body)
	}
	for _, bad := range []map[string]string{{"MCP-Protocol-Version": mcpProtocolVersion}, {"MCP-Protocol-Version": tasksProtocolVersion, "Authorization": "Bearer wrong"}} {
		w = call("tasks/get", taskParams(handle), bad)
		if w.Code == 200 && !bytes.Contains(w.Body.Bytes(), []byte(`"error"`)) {
			t.Fatalf("invalid request dispatched: %s", w.Body)
		}
	}
	w = call("tasks/get", map[string]any{"taskId": handle}, headers)
	if w.Code != http.StatusBadRequest || !bytes.Contains(w.Body.Bytes(), []byte(`"code":-32020`)) {
		t.Fatal("modern metadata not required")
	}
	params := taskParams(handle)
	params["_meta"].(map[string]any)["io.modelcontextprotocol/clientCapabilities"] = map[string]any{}
	w = call("tasks/get", params, headers)
	if w.Code != http.StatusBadRequest || !bytes.Contains(w.Body.Bytes(), []byte(`"code":-32021`)) {
		t.Fatalf("missing capability: %d %s", w.Code, w.Body)
	}
}

func TestTasksHTTPRoutingValidation(t *testing.T) {
	upstreamCalls := 0
	upstream := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		http.Error(w, "unexpected dispatch", http.StatusInternalServerError)
	}))
	handler, err := newHTTPHandler(&server{api: newAPIClient(upstream.URL, "admin")}, "listener-token", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"tools/call", "tasks/get", "tasks/update", "tasks/cancel"} {
		params := taskParams(taskHandle("run"))
		name := taskHandle("run")
		if method == "tools/call" {
			params = taskStartParams("start_run")
			name = "start_run"
		}
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		for _, field := range []string{"MCP-Protocol-Version", "Mcp-Method", "Mcp-Name"} {
			for _, value := range []string{"", "conflicting"} {
				headers := map[string]string{"MCP-Protocol-Version": tasksProtocolVersion, "Mcp-Method": method, "Mcp-Name": name}
				headers[field] = value
				w := httpMCPRequest(handler, http.MethodPost, string(body), headers)
				var reply jsonRPCResponse
				_ = json.Unmarshal(w.Body.Bytes(), &reply)
				if w.Code != http.StatusBadRequest || reply.Error == nil || reply.Error.Code != -32020 {
					t.Fatalf("%s %s=%q: %d %s", method, field, value, w.Code, w.Body)
				}
			}
		}
	}
	if upstreamCalls != 0 {
		t.Fatalf("invalid headers caused %d upstream operations", upstreamCalls)
	}
	for _, field := range []string{"MCP-Protocol-Version", "Mcp-Method", "Mcp-Name"} {
		headers := http.Header{}
		headers.Set("MCP-Protocol-Version", tasksProtocolVersion)
		headers.Set("Mcp-Method", "tasks/get")
		headers.Set("Mcp-Name", taskHandle("run"))
		headers.Add(field, headers.Get(field))
		raw, _ := json.Marshal(taskParams(taskHandle("run")))
		if validateModernHeaders(headers, jsonRPCRequest{Method: "tasks/get", Params: raw}, tasksProtocolVersion) == "" {
			t.Fatalf("duplicate %s accepted", field)
		}
	}
	// An encoded routing name is compared after decoding, not as literal text.
	params := taskStartParams("unknown")
	params["name"] = "unknown"
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": params})
	headers := map[string]string{"MCP-Protocol-Version": tasksProtocolVersion, "Mcp-Method": "tools/call", "Mcp-Name": "=?base64?" + base64.StdEncoding.EncodeToString([]byte("unknown")) + "?="}
	w := httpMCPRequest(handler, http.MethodPost, string(body), headers)
	if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte(`"isError":true`)) {
		t.Fatalf("encoded name: %d %s", w.Code, w.Body)
	}
	body, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tasks/list", "params": map[string]any{"_meta": taskMeta()}})
	headers["Mcp-Method"] = "tasks/list"
	w = httpMCPRequest(handler, http.MethodPost, string(body), headers)
	if w.Code != http.StatusNotFound || !bytes.Contains(w.Body.Bytes(), []byte(`"code":-32601`)) {
		t.Fatalf("unknown modern method: %d %s", w.Code, w.Body)
	}
}

func TestTasksReadBackFallbackAndRequestCapabilities(t *testing.T) {
	// A successful submission whose durable record cannot be read must remain an
	// ordinary success, preserving the run ID without exposing a broken handle.
	upstream := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"runId":"committed","state":"running"}`))
		} else {
			http.Error(w, "not visible", http.StatusNotFound)
		}
	}))
	s := &server{api: newAPIClient(upstream.URL, "admin")}
	got, err := taskRPC(t, s, "tools/call", taskStartParams("start_run"))
	if err != nil || got["resultType"] != "complete" || got["isError"] == true || got["taskId"] != nil {
		t.Fatalf("read-back fallback: %v %v", got, err)
	}
	if !bytes.Contains([]byte(got["content"].([]any)[0].(map[string]any)["text"].(string)), []byte("committed")) {
		t.Fatal("lost committed run ID")
	}
	store := state.New()
	_ = store.CreateRun(&state.Run{ID: "input"})
	_ = store.CreateInputRequest(&state.InputRequest{RunID: "input", RequestID: "req", Name: "Provide structured input", Schema: json.RawMessage(`{"type":"object","properties":{"nested":{"type":"array"}}}`)})
	upstream = newTestServer(t, api.WithAuth(api.NewRouter(store, &taskRunner{store: store}), "admin", "worker"))
	s = &server{api: newAPIClient(upstream.URL, "admin")}
	params := taskParams(taskHandle("input"))
	caps := params["_meta"].(map[string]any)["io.modelcontextprotocol/clientCapabilities"].(map[string]any)
	delete(caps, "elicitation")
	if _, err := taskRPC(t, s, "tasks/get", params); err == nil || err.Code != -32021 {
		t.Fatalf("undeclared form: %v", err)
	}
	caps["elicitation"] = map[string]any{} // Backwards-compatible form declaration.
	got, err = taskRPC(t, s, "tasks/get", params)
	if err != nil || got["status"] != "input_required" {
		t.Fatalf("empty elicitation declaration: %v %v", got, err)
	}
	schema := got["inputRequests"].(map[string]any)["req"].(map[string]any)["params"].(map[string]any)["requestedSchema"].(map[string]any)
	if schema["properties"].(map[string]any)["json"].(map[string]any)["type"] != "string" {
		t.Fatalf("invalid MCP form schema: %v", schema)
	}
	params["inputResponses"] = map[string]any{"req": map[string]any{"action": "accept", "content": map[string]any{"json": `{"nested":[false,null]}`}}}
	if _, err := taskRPC(t, s, "tasks/update", params); err != nil {
		t.Fatal(err)
	}
	req, _ := store.GetInputRequest("input", "req")
	if req.State != state.InputRequestFulfilled {
		t.Fatalf("nested JSON response: %+v", req)
	}
	// Capabilities from the prior request must never be inherited.
	delete(caps, "extensions")
	if _, err := taskRPC(t, s, "tasks/get", params); err == nil || err.Code != -32021 {
		t.Fatalf("inherited task capability: %v", err)
	}
	caps["extensions"] = map[string]any{tasksExtension: map[string]any{}}
	delete(caps, "elicitation")
	if _, err := taskRPC(t, s, "tasks/cancel", params); err != nil {
		t.Fatalf("cancel requires no form capability: %v", err)
	}
}
