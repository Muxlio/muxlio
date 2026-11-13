package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/muxlio/muxlio-core/internal/api"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadAccessGrantsUsesCredentialFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(filepath.Join(dir, "client-token"), []byte("scoped-credential\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"accessGrants":[{"tokenFile":"client-token","runId":"own","permissions":["control"]}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	grants, err := readAccessGrants(path)
	if err != nil || len(grants) != 1 || grants[0].Token != "scoped-credential" || grants[0].RunID != "own" {
		t.Fatalf("bad grant: %v", err)
	}
	raw, _ := json.Marshal(grants)
	if strings.Contains(string(raw), "scoped-credential") {
		t.Fatal("credential serialized")
	}
	if err := os.Remove(filepath.Join(dir, "client-token")); err != nil {
		t.Fatal(err)
	}
	if _, err := readAccessGrants(path); err == nil {
		t.Fatal("missing credential file accepted")
	}
}

func TestWorkerCredentialFilesReloadWithoutCoordinatorRestart(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "config.json")
	tokenFile := filepath.Join(dir, "worker-token")
	write := func(path, value string) {
		t.Helper()
		if err := os.WriteFile(path+".tmp", []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(path+".tmp", path); err != nil {
			t.Fatal(err)
		}
	}
	write(tokenFile, "old")
	write(config, `{"accessGrants":[{"tokenFile":"worker-token","workerId":"one","permissions":["worker"]}]}`)
	h := api.WithReloadingAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }), "admin", func() (string, []api.AccessGrant, error) {
		grants, err := readAccessGrants(config)
		return "", grants, err
	})
	check := func(token string, want int) {
		t.Helper()
		r := httptest.NewRequest("GET", "/v1/workers/one/assignments", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("credential %q: %d, want %d", token, w.Code, want)
		}
	}
	check("old", 204)
	write(tokenFile, "new")
	check("old", 401)
	check("new", 204)
	write(config, `{"accessGrants":[{"tokenFile":"worker-token","workerId":"one","permissions":["worker"],"revoked":true}]}`)
	check("new", 403)
	write(config, `{`)
	check("new", 503)
}

func TestServiceUILimitsAutomaticCredentialsAndRespectsExplicitScopes(t *testing.T) {
	for _, tc := range []struct {
		name, peer, host, token, origin, site, path string
		emptyAuth                                   bool
		want                                        int
	}{
		{name: "private network UI without credentials", peer: "192.168.1.2:9", host: "192.168.1.1:7600", want: 204},
		{name: "remote scoped cancel another run", peer: "203.0.113.1:9", token: "scoped", want: 403},
		{name: "local scoped cancel another run", peer: "127.0.0.1:9", token: "scoped", want: 403},
		{name: "remote scoped answer another run", peer: "203.0.113.1:9", token: "scoped", path: "/runs/other/signals/answer", want: 403},
		{name: "worker cannot control", peer: "127.0.0.1:9", token: "worker", want: 403},
		{name: "invalid local credential", peer: "127.0.0.1:9", token: "invalid", want: 401},
		{name: "empty explicit local credential", peer: "127.0.0.1:9", emptyAuth: true, want: 401},
		{name: "malformed local credential", peer: "127.0.0.1:9", token: " ", want: 401},
		{name: "scoped own cancel", peer: "203.0.113.1:9", token: "scoped", path: "/runs/own/cancel", want: 204},
		{name: "remote administrator", peer: "203.0.113.1:9", token: "admin", want: 204},
		{name: "local anonymous UI", peer: "127.0.0.1:9", want: 204},
		{name: "local same origin UI", peer: "127.0.0.1:9", origin: "http://localhost", want: 204},
		{name: "cross origin UI", peer: "127.0.0.1:9", origin: "https://evil.example", want: 403},
		{name: "null origin UI", peer: "127.0.0.1:9", origin: "null", want: 403},
		{name: "cross site UI", peer: "127.0.0.1:9", site: "cross-site", want: 403},
		{name: "untrusted host", peer: "127.0.0.1:9", host: "evil.example", want: 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := serviceUI(api.WithAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasPrefix(r.URL.Path, "/v1/") {
					t.Fatal("API path was not preserved")
				}
				w.WriteHeader(204)
			}), "admin", "worker", api.AccessGrant{Token: "scoped", RunID: "own", Permissions: []string{"control", "signals"}}), "", "admin")
			path := tc.path
			if path == "" {
				path = "/runs/other/cancel"
			}
			req := httptest.NewRequest("POST", "http://localhost/v1"+path, nil)
			req.RemoteAddr = tc.peer
			if tc.host != "" {
				req.Host = tc.host
			}
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			if tc.emptyAuth {
				req.Header.Set("Authorization", "")
			}
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			req.Header.Set("Sec-Fetch-Site", tc.site)
			resp := httptest.NewRecorder()
			h.ServeHTTP(resp, req)
			if resp.Header().Get("X-Frame-Options") != "DENY" {
				t.Error("UI can be framed")
			}
			if resp.Code != tc.want {
				t.Fatalf("status=%d want=%d", resp.Code, tc.want)
			}
		})
	}
}

func TestServerBodyLimitAllowsLargeArtifacts(t *testing.T) {
	for _, path := range []string{"/v1/artifacts/sha256:test", "/v1/runs"} {
		t.Run(path, func(t *testing.T) {
			h := withRequestBodyLimit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n, err := io.Copy(io.Discard, r.Body)
				if strings.HasPrefix(path, "/v1/artifacts/") {
					if err != nil || n != 9<<20 {
						t.Errorf("artifact body: %d %v", n, err)
					}
				} else {
					var limitErr *http.MaxBytesError
					if !errors.As(err, &limitErr) {
						t.Errorf("control body not bounded: %v", err)
					}
				}
			}))
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPut, path, strings.NewReader(strings.Repeat("x", 9<<20))))
		})
	}
}
