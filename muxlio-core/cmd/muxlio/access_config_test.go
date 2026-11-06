package main

import (
	"encoding/json"
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

func TestServiceUIRespectsAuthorizationScopes(t *testing.T) {
	for _, tc := range []struct {
		name, peer, host, token, origin, site, path string
		want                                        int
	}{
		{name: "remote anonymous cancel", peer: "203.0.113.1:9", want: 401},
		{name: "remote scoped cancel another run", peer: "203.0.113.1:9", token: "scoped", want: 403},
		{name: "local scoped cancel another run", peer: "127.0.0.1:9", token: "scoped", want: 403},
		{name: "remote scoped answer another run", peer: "203.0.113.1:9", token: "scoped", path: "/runs/other/signals/answer", want: 403},
		{name: "worker cannot control", peer: "127.0.0.1:9", token: "worker", want: 403},
		{name: "invalid local credential", peer: "127.0.0.1:9", token: "invalid", want: 401},
		{name: "scoped own cancel", peer: "203.0.113.1:9", token: "scoped", path: "/runs/own/cancel", want: 204},
		{name: "remote administrator", peer: "203.0.113.1:9", token: "admin", want: 204},
		{name: "local anonymous UI", peer: "127.0.0.1:9", want: 204},
		{name: "local same origin UI", peer: "127.0.0.1:9", origin: "http://localhost", want: 204},
		{name: "cross origin UI", peer: "127.0.0.1:9", origin: "https://evil.example", want: 401},
		{name: "null origin UI", peer: "127.0.0.1:9", origin: "null", want: 401},
		{name: "cross site UI", peer: "127.0.0.1:9", site: "cross-site", want: 401},
		{name: "untrusted host", peer: "127.0.0.1:9", host: "evil.example", want: 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := serviceUI(api.WithAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasPrefix(r.URL.Path, "/v1/") {
					t.Fatal("proxy path was not rewritten")
				}
				w.WriteHeader(204)
			}), "admin", "worker", api.AccessGrant{Token: "scoped", RunID: "own", Permissions: []string{"control", "signals"}}), "", "admin")
			path := tc.path
			if path == "" {
				path = "/runs/other/cancel"
			}
			req := httptest.NewRequest("POST", "http://localhost/_ui-api/v1"+path, nil)
			req.RemoteAddr = tc.peer
			if tc.host != "" {
				req.Host = tc.host
			}
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			req.Header.Set("Origin", tc.origin)
			req.Header.Set("Sec-Fetch-Site", tc.site)
			resp := httptest.NewRecorder()
			h.ServeHTTP(resp, req)
			if resp.Code != tc.want {
				t.Fatalf("status=%d want=%d", resp.Code, tc.want)
			}
		})
	}
}
