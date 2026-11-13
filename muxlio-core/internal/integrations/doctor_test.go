package integrations

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCoordinatorDoctorDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body, want string
	}{
		{"ok", 200, `{"registeredWorkers":[]}`, ""},
		{"unauthorized", 401, "secret response", "authentication rejected"},
		{"forbidden", 403, "secret response", "lacks workers read permission"},
		{"wrong endpoint", 200, `{"status":"ok"}`, "unexpected workers response"},
		{"redirect", 302, "secret response", "redirects are refused"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := fixture(t, "cursor", "project", "stdio")
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/workers" || r.Header.Get("Authorization") != "Bearer secret-token" {
					t.Error("wrong endpoint or credentials")
				}
				w.Header().Set("Location", "https://unknown.example")
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer upstream.Close()
			raw, _ := json.Marshal(map[string]any{"defaultServer": "local", "token": "secret-token", "servers": map[string]any{"local": map[string]string{"url": upstream.URL, "credentialRef": "root"}}})
			writeFixture(t, o.Config, raw)
			_, err := CheckCoordinator(context.Background(), o.Config, "", "", "", true)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "secret") {
				t.Fatalf("diagnostic: %v", err)
			}
		})
	}
	o := fixture(t, "cursor", "project", "stdio")
	writeFixture(t, o.Config, []byte(`{"defaultServer":"remote","token":"admin","servers":{"remote":{"url":"https://example.test"}}}`))
	if _, err := CheckCoordinator(context.Background(), o.Config, "", "", "", false); err == nil || !strings.Contains(err.Error(), "no credential") {
		t.Fatal(err)
	}
}
