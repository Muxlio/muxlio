package mcp

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func httpMCPFixture(t *testing.T, origins ...string) (http.Handler, *server) {
	t.Helper()
	upstream := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer upstream-token" {
			http.Error(w, "wrong upstream credential", http.StatusUnauthorized)
			return
		}
		newCoordinatorMux().ServeHTTP(w, r)
	}))
	tools := &server{api: newAPIClient(upstream.URL, "upstream-token")}
	handler, err := newHTTPHandler(tools, "listener-token", origins)
	if err != nil {
		t.Fatal(err)
	}
	return handler, tools
}

func httpMCPRequest(handler http.Handler, method, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/mcp", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer listener-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", mcpProtocolVersion)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func TestHTTPTransportLifecycle(t *testing.T) {
	handler, _ := httpMCPFixture(t)
	for _, version := range []string{mcpProtocolVersion, "unknown", "2024-11-05", "2025-03-26", "2025-06-18"} {
		body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"` + version + `","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`
		w := httpMCPRequest(handler, http.MethodPost, body, map[string]string{"MCP-Protocol-Version": ""})
		if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("initialization: %d %s", w.Code, w.Body)
		}
		var result struct {
			Result initializeResult `json:"result"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Result.ProtocolVersion != mcpProtocolVersion || result.Result.Instructions == "" || w.Header().Get("MCP-Session-Id") != "" {
			t.Fatalf("invalid negotiation: %s", w.Body)
		}
	}
	w := httpMCPRequest(handler, http.MethodPost, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, nil)
	if w.Code != http.StatusAccepted || w.Body.Len() != 0 {
		t.Fatalf("notification: %d %s", w.Code, w.Body)
	}
	w = httpMCPRequest(handler, http.MethodPost, `{"jsonrpc":"2.0","id":"list","method":"tools/list"}`, nil)
	var listed struct {
		ID     string          `json:"id"`
		Result toolsListResult `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || listed.ID != "list" || len(listed.Result.Tools) != len(tools) {
		t.Fatalf("tools/list: %d %s", w.Code, w.Body)
	}
	for _, tool := range listed.Result.Tools {
		if tool.Annotations == nil {
			t.Errorf("tool %s lost annotations", tool.Name)
		}
	}
	w = httpMCPRequest(handler, http.MethodPost, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_workers","arguments":{}}}`, nil)
	var called struct {
		Result toolCallResult `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &called); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || called.Result.IsError || !strings.Contains(called.Result.Content[0].Text, "registeredWorkers") {
		t.Fatalf("tool call or upstream authentication failed: %s", w.Body)
	}
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		w = httpMCPRequest(handler, method, "", nil)
		if w.Code != http.StatusMethodNotAllowed || !strings.Contains(w.Header().Get("Allow"), "POST") {
			t.Fatalf("stateless %s: %d", method, w.Code)
		}
	}
}

func TestHTTPAuthAndOrigins(t *testing.T) {
	handler, tools := httpMCPFixture(t, "https://inspector.example")
	for _, tc := range []struct {
		name    string
		headers map[string]string
		status  int
	}{
		{"missing credential", map[string]string{"Authorization": ""}, http.StatusUnauthorized},
		{"incorrect credential", map[string]string{"Authorization": "Bearer wrong"}, http.StatusUnauthorized},
		{"upstream token cannot replace listener token", map[string]string{"Authorization": "Bearer upstream-token"}, http.StatusUnauthorized},
		{"invalid scheme", map[string]string{"Authorization": "Basic listener-token"}, http.StatusUnauthorized},
		{"allowed origin", map[string]string{"Origin": "https://inspector.example"}, http.StatusOK},
		{"hostile origin", map[string]string{"Origin": "https://hostile.example"}, http.StatusForbidden},
		{"opaque origin", map[string]string{"Origin": "null"}, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httpMCPRequest(handler, http.MethodPost, `{"jsonrpc":"2.0","id":1,"method":"ping"}`, tc.headers)
			if w.Code != tc.status {
				t.Fatalf("got %d: %s", w.Code, w.Body)
			}
			if tc.status == http.StatusUnauthorized && w.Header().Get("WWW-Authenticate") == "" {
				t.Fatal("missing bearer challenge")
			}
		})
	}
	w := httpMCPRequest(handler, http.MethodOptions, "", map[string]string{"Authorization": "", "Origin": "https://inspector.example"})
	if w.Code != http.StatusNoContent || w.Header().Get("Access-Control-Allow-Origin") != "https://inspector.example" {
		t.Fatalf("browser preflight: %d %s", w.Code, w.Body)
	}
	if _, err := newHTTPHandler(tools, "", nil); err == nil {
		t.Fatal("unauthenticated listener allowed")
	}
	for _, origin := range []string{"*", "null", "https://example.com/path", "https://user:pass@example.com"} {
		if _, err := newHTTPHandler(tools, "token", []string{origin}); err == nil {
			t.Errorf("accepted unsafe allowed Origin %q", origin)
		}
	}
	defaultOrigins, _ := newHTTPHandler(tools, "listener-token", nil)
	w = httpMCPRequest(defaultOrigins, http.MethodPost, `{"jsonrpc":"2.0","id":1,"method":"ping"}`, map[string]string{"Origin": "https://inspector.example"})
	if w.Code != http.StatusForbidden {
		t.Fatal("browser origin allowed without explicit configuration")
	}
}

func TestHTTPInvalidMessagesDoNotDispatch(t *testing.T) {
	handler, _ := httpMCPFixture(t)
	for _, tc := range []struct {
		name, body string
		headers    map[string]string
		status     int
		rpcCode    int
	}{
		{"malformed", `{`, nil, http.StatusBadRequest, -32700},
		{"batch", `[{"jsonrpc":"2.0","id":1,"method":"ping"}]`, nil, http.StatusBadRequest, -32600},
		{"null", `null`, nil, http.StatusBadRequest, -32600},
		{"bad protocol", `{"jsonrpc":"1.0","id":1,"method":"ping"}`, nil, http.StatusBadRequest, -32600},
		{"null request ID", `{"jsonrpc":"2.0","id":null,"method":"ping"}`, nil, http.StatusBadRequest, -32600},
		{"request as notification", `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"start_run","arguments":{}}}`, nil, http.StatusBadRequest, -32600},
		{"trailing data", `{"jsonrpc":"2.0","id":1,"method":"ping"} {}`, nil, http.StatusBadRequest, -32700},
		{"mixed request and response", `{"jsonrpc":"2.0","id":1,"method":"ping","result":{}}`, nil, http.StatusBadRequest, -32600},
		{"unknown method", `{"jsonrpc":"2.0","id":1,"method":"unknown"}`, nil, http.StatusOK, -32601},
		{"invalid initialization", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`, nil, http.StatusOK, -32602},
		{"unsupported header", `{"jsonrpc":"2.0","id":1,"method":"ping"}`, map[string]string{"MCP-Protocol-Version": "wrong"}, http.StatusBadRequest, 0},
		{"missing version after initialization", `{"jsonrpc":"2.0","id":1,"method":"ping"}`, map[string]string{"MCP-Protocol-Version": ""}, http.StatusBadRequest, 0},
		{"legacy March version", `{"jsonrpc":"2.0","id":1,"method":"ping"}`, map[string]string{"MCP-Protocol-Version": "2025-03-26"}, http.StatusBadRequest, 0},
		{"legacy June version", `{"jsonrpc":"2.0","id":1,"method":"ping"}`, map[string]string{"MCP-Protocol-Version": "2025-06-18"}, http.StatusBadRequest, 0},
		{"wrong content type", `{}`, map[string]string{"Content-Type": "text/plain"}, http.StatusUnsupportedMediaType, 0},
		{"missing accept", `{}`, map[string]string{"Accept": ""}, http.StatusNotAcceptable, 0},
		{"missing stream accept", `{}`, map[string]string{"Accept": "application/json"}, http.StatusNotAcceptable, 0},
		{"zero accept quality", `{}`, map[string]string{"Accept": "application/json;q=0, text/event-stream"}, http.StatusNotAcceptable, 0},
		{"client response", `{"jsonrpc":"2.0","id":1,"result":{}}`, nil, http.StatusAccepted, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httpMCPRequest(handler, http.MethodPost, tc.body, tc.headers)
			if w.Code != tc.status {
				t.Fatalf("got %d want %d: %s", w.Code, tc.status, w.Body)
			}
			if tc.rpcCode != 0 {
				var result jsonRPCResponse
				if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if result.Error == nil || result.Error.Code != tc.rpcCode {
					t.Fatalf("wrong RPC error: %s", w.Body)
				}
				if tc.status == http.StatusBadRequest && !bytes.Contains(w.Body.Bytes(), []byte(`"id":null`)) {
					t.Fatal("invalid requests must have null response ID")
				}
			}
		})
	}
	w := httpMCPRequest(handler, http.MethodPost, strings.Repeat("x", maxMCPRequestBytes+1), nil)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized request: %d", w.Code)
	}
	w = httpMCPRequest(handler, http.MethodPost, string([]byte{0xff}), nil)
	if w.Code != http.StatusBadRequest {
		t.Fatal("invalid UTF-8 accepted")
	}
}

func TestHTTPConcurrentRequests(t *testing.T) {
	handler, _ := httpMCPFixture(t)
	ts := httptest.NewServer(handler)
	defer ts.Close()
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":`+strconv.Itoa(id)+`,"method":"tools/list"}`))
			req.Header.Set("Authorization", "Bearer listener-token")
			req.Header.Set("Accept", "application/json, text/event-stream")
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("MCP-Protocol-Version", mcpProtocolVersion)
			resp, err := ts.Client().Do(req)
			if err != nil {
				t.Error(err)
				return
			}
			defer resp.Body.Close()
			var result struct {
				ID     int             `json:"id"`
				Result toolsListResult `json:"result"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				t.Error(err)
				return
			}
			if resp.StatusCode != http.StatusOK || result.ID != id || len(result.Result.Tools) != len(tools) {
				t.Errorf("concurrent request %d received another request's response", id)
			}
		}(i)
	}
	wg.Wait()
}

func TestHTTPListenerTokenPrecedence(t *testing.T) {
	t.Setenv("MUXLIO_MCP_HTTP_TOKEN", "")
	got, err := httpListenerToken("", "upstream")
	if err != nil || got != "upstream" {
		t.Fatal("coordinator token fallback failed")
	}
	t.Setenv("MUXLIO_MCP_HTTP_TOKEN", "listener")
	got, err = httpListenerToken("", "upstream")
	if err != nil || got != "listener" {
		t.Fatal("listener environment override failed")
	}
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte(" file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = httpListenerToken(file, "upstream")
	if err != nil || got != "file-token" {
		t.Fatal("token file override failed")
	}
	if _, err := httpListenerToken(file+"-missing", "upstream"); err == nil {
		t.Fatal("missing token file ignored")
	}
}

func TestHTTPServerShutdown(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serveMCPHTTP(ctx, listener, http.NotFoundHandler()) }()
	resp, err := http.Get("http://" + listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not shut down")
	}
}

func TestHTTPTLSListener(t *testing.T) {
	handler, _ := httpMCPFixture(t)
	identity := httptest.NewTLSServer(http.NotFoundHandler())
	defer identity.Close()
	pair := identity.TLS.Certificates[0]
	cert := filepath.Join(t.TempDir(), "cert.pem")
	key := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: pair.Certificate[0]}), 0o600); err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(pair.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, files := range [][2]string{{cert, ""}, {"", key}, {cert + "-missing", key}} {
		if listener, err := listenMCPHTTP("127.0.0.1:0", files[0], files[1]); err == nil {
			listener.Close()
			t.Fatal("invalid TLS files accepted")
		}
	}
	listener, err := listenMCPHTTP("127.0.0.1:0", cert, key)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serveMCPHTTP(ctx, listener, handler) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	root, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(root)
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	req, _ := http.NewRequest(http.MethodPost, "https://"+listener.Addr().String()+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Authorization", "Bearer listener-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", mcpProtocolVersion)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var result struct {
		Result toolsListResult `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || len(result.Result.Tools) != len(tools) || resp.TLS == nil {
		t.Fatal("TLS MCP request failed")
	}
}

func TestHTTPClientCredentialsAreIndependent(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("Authorization")
		if r.URL.Path == "/v1/auth" {
			if token == "Bearer reader" || token == "Bearer controller" {
				w.WriteHeader(204)
			} else {
				w.WriteHeader(401)
			}
			return
		}
		// Generic coordinator fixture: scopes are enforced at the API boundary.
		if r.URL.Path == "/v1/runs/own" && token == "Bearer reader" {
			w.Write([]byte(`{"id":"own"}`))
			return
		}
		if r.URL.Path == "/v1/runs/own/cancel" && token == "Bearer controller" {
			w.Write([]byte(`{}`))
			return
		}
		w.WriteHeader(403)
	}))
	t.Cleanup(upstream.Close)
	handler, err := newHTTPHandler(&server{api: newAPIClient(upstream.URL, "admin")}, "listener", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		token, tool string
		denied      bool
	}{
		{"reader", "get_run", false}, {"reader", "cancel_run", true},
		{"controller", "cancel_run", false}, {"controller", "get_run", true},
	} {
		t.Run(tc.token+tc.tool, func(t *testing.T) {
			t.Parallel()
			w := httpMCPRequest(handler, "POST", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+tc.tool+`","arguments":{"runId":"own"}}}`, map[string]string{"Authorization": "Bearer " + tc.token})
			if w.Code != 200 || strings.Contains(w.Body.String(), `"isError":true`) != tc.denied {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
		})
	}
}

func TestHTTPListenerCredentialRotation(t *testing.T) {
	file := filepath.Join(t.TempDir(), "token")
	write := func(token string) {
		t.Helper()
		if err := os.WriteFile(file, []byte(token), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("old")
	handler, err := NewHTTPHandler("http://127.0.0.1:1", "", file, nil)
	if err != nil {
		t.Fatal(err)
	}
	check := func(token string, want int) {
		t.Helper()
		w := httpMCPRequest(handler, "POST", `{"jsonrpc":"2.0","id":1,"method":"ping"}`, map[string]string{"Authorization": "Bearer " + token})
		if w.Code != want {
			t.Fatalf("got %d want %d: %s", w.Code, want, w.Body)
		}
	}
	check("old", 200)
	write("new")
	check("old", 401)
	check("new", 200)
	write("")
	check("new", 503)
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	check("new", 503)
}

func TestHTTPRejectsLocalFileAuthoring(t *testing.T) {
	handler, _ := httpMCPFixture(t)
	for _, name := range []string{"build", "test"} {
		for _, args := range []string{`{"trustedSource":true,"path":"/tmp/remote.ts"}`, `{"path":"/tmp/private.json"}`} {
			w := httpMCPRequest(handler, "POST", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+name+`","arguments":`+args+`}}`, nil)
			if w.Code != 403 {
				t.Fatalf("%s: %d %s", name, w.Code, w.Body)
			}
		}
	}
}

func TestHTTPRejectsAmbiguousCredentials(t *testing.T) {
	handler, _ := httpMCPFixture(t)
	r := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	r.Header.Add("Authorization", "Bearer listener-token")
	r.Header.Add("Authorization", "Bearer reader")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("ambiguous credentials accepted: %d %s", w.Code, w.Body)
	}
}
