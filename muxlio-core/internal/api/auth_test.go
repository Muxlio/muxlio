package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-core/internal/artifacts"
	"github.com/muxlio/muxlio-core/internal/state"
)

func authRequest(method, path, token, remoteAddr string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if remoteAddr != "" {
		req.RemoteAddr = remoteAddr
	}
	return req
}

func TestAuthClientTokenControlAPI(t *testing.T) {
	const client = "client-secret"
	const worker = "worker-secret"
	handler := WithAuth(NewRouter(state.New(), nil), client, worker)

	t.Run("client lists runs", func(t *testing.T) {
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, authRequest(http.MethodGet, "/v1/runs", client, "203.0.113.1:1234"))
		if resp.Code != http.StatusOK {
			t.Fatalf("status = %d body %s", resp.Code, resp.Body.String())
		}
	})

	t.Run("missing token rejected off loopback", func(t *testing.T) {
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, authRequest(http.MethodGet, "/v1/runs", "", "203.0.113.1:1234"))
		if resp.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d", resp.Code)
		}
	})
}

func TestAuthWorkerTokenProtocol(t *testing.T) {
	const client = "client-secret"
	const worker = "worker-secret"
	backend := &workerBackendFixture{}
	protocol := NewWorkerProtocolHandler(backend)
	handler := WithAuth(NewRouter(state.New(), nil, protocol), client, worker)
	regBody := `{"protocolVersion":"v1","workerId":"remote","capacity":1,"capabilities":[{"name":"test"}]}`

	t.Run("worker token off loopback", func(t *testing.T) {
		resp := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/workers/register", strings.NewReader(regBody))
		req.Header.Set("Authorization", "Bearer "+worker)
		req.RemoteAddr = "203.0.113.1:4444"
		handler.ServeHTTP(resp, req)
		if resp.Code != http.StatusOK {
			t.Fatalf("register status = %d: %s", resp.Code, resp.Body.String())
		}
	})

	t.Run("client token off loopback rejected for worker register", func(t *testing.T) {
		resp := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/workers/register", strings.NewReader(regBody))
		req.Header.Set("Authorization", "Bearer "+client)
		req.RemoteAddr = "203.0.113.1:4444"
		handler.ServeHTTP(resp, req)
		if resp.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", resp.Code)
		}
	})

	t.Run("client token loopback allowed for worker register", func(t *testing.T) {
		resp := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/workers/register", strings.NewReader(regBody))
		req.Header.Set("Authorization", "Bearer "+client)
		req.RemoteAddr = "127.0.0.1:7600"
		handler.ServeHTTP(resp, req)
		if resp.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", resp.Code, resp.Body.String())
		}
	})

	t.Run("worker heartbeat off loopback", func(t *testing.T) {
		resp := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/workers/remote/heartbeat", strings.NewReader(`{"protocolVersion":"v1"}`))
		req.Header.Set("Authorization", "Bearer "+worker)
		req.RemoteAddr = "198.51.100.9:1234"
		handler.ServeHTTP(resp, req)
		if resp.Code != http.StatusOK {
			t.Fatalf("heartbeat status = %d: %s", resp.Code, resp.Body.String())
		}
	})
}

func TestAuthWorkerTokenCannotUseControlAPI(t *testing.T) {
	const client = "client-secret"
	const worker = "worker-secret"
	handler := WithAuth(NewRouter(state.New(), nil), client, worker)

	cases := []struct {
		method, path string
	}{
		{http.MethodGet, "/v1/runs"},
		{http.MethodPost, "/v1/runs"},
		{http.MethodGet, "/v1/runs/run-1"},
		{http.MethodPost, "/v1/runs/run-1/pause"},
		{http.MethodPost, "/v1/runs/run-1/resume"},
		{http.MethodPost, "/v1/runs/run-1/cancel"},
		{http.MethodGet, "/v1/workers"},
	}
	for _, tc := range cases {
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, authRequest(tc.method, tc.path, worker, "203.0.113.1:1"))
		if resp.Code != http.StatusForbidden {
			t.Fatalf("%s %s with worker token: status = %d, want 403", tc.method, tc.path, resp.Code)
		}
	}
}

func TestAuthWorkerTokenArtifactAccess(t *testing.T) {
	const client = "client-secret"
	const worker = "worker-secret"
	driver, err := artifacts.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	handler := WithAuth(NewRouterWithArtifacts(state.New(), nil, driver), client, worker)
	digest := "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, authRequest(http.MethodGet, "/v1/artifacts/"+digest+"/transfer?method=GET", worker, "203.0.113.1:9"))
	if resp.Code != http.StatusOK {
		t.Fatalf("worker artifact transfer plan status = %d: %s", resp.Code, resp.Body.String())
	}

	resp = httptest.NewRecorder()
	handler.ServeHTTP(resp, authRequest(http.MethodGet, "/v1/artifacts/"+digest+"/transfer?method=GET", client, "203.0.113.1:9"))
	if resp.Code != http.StatusOK {
		t.Fatalf("client artifact transfer plan status = %d: %s", resp.Code, resp.Body.String())
	}
}

func TestIsWorkerProtocolPath(t *testing.T) {
	if !isWorkerProtocolPath("/v1/workers/register", http.MethodPost) {
		t.Fatal("register should be worker protocol")
	}
	if isWorkerProtocolPath("/v1/workers", http.MethodGet) {
		t.Fatal("worker list is control API")
	}
	if !isWorkerProtocolPath("/v1/workers/w1/assignments/complete", http.MethodPost) {
		t.Fatal("complete should be worker protocol")
	}
	if !isWorkerProtocolPath("/v1/workers/w1/assignments/accounting", http.MethodPost) {
		t.Fatal("accounting append should be worker protocol")
	}
}

func TestAuthWorkerTokenAccountingAppend(t *testing.T) {
	const client = "client-secret"
	const worker = "worker-secret"
	backend := &workerBackendFixture{}
	protocol := NewWorkerProtocolHandler(backend)
	handler := WithAuth(NewRouter(state.New(), nil, protocol), client, worker)

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/workers/remote/assignments/accounting", strings.NewReader(`{"protocolVersion":"v1"}`))
	req.Header.Set("Authorization", "Bearer "+worker)
	req.RemoteAddr = "203.0.113.1:4444"
	handler.ServeHTTP(resp, req)
	if resp.Code == http.StatusForbidden {
		t.Fatalf("worker token must reach accounting endpoint, got 403: %s", resp.Body.String())
	}
}
