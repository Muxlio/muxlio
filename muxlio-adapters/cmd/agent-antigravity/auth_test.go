package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAntigravityAuthenticationAvailability(t *testing.T) {
	for _, tc := range []struct {
		name, authType, filename, content string
		subDir                            string
		antigravityKey                    string
		ready                             bool
	}{
		{name: "missing"},
		{name: "environment antigravity key", antigravityKey: "secret-api-key", ready: true},
		{name: "whitespace key", antigravityKey: "  "},
		{name: "saved antigravity key", authType: "antigravity-api-key", filename: "antigravity-credentials.json", content: "encrypted-secret", ready: true},
		{name: "saved generic api key", authType: "api-key", filename: "credentials.json", content: "encrypted-secret", ready: true},
		{name: "cached oauth", authType: "oauth-personal", filename: "oauth_creds.json", content: `{"refresh_token":"secret"}`, ready: true},
		{name: "cached generic oauth", authType: "oauth", filename: "oauth-credentials.json", content: `{"refresh_token":"secret"}`, ready: true},
		{name: "settings alone", authType: "antigravity-api-key"},
		{name: "empty credentials", authType: "antigravity-api-key", filename: "antigravity-credentials.json"},
		{name: "wrong credential type", authType: "oauth-personal", filename: "antigravity-credentials.json", content: "encrypted-secret"},
		{name: "unselected credentials", filename: "antigravity-credentials.json", content: "encrypted-secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("ANTIGRAVITY_CLI_HOME", home)
			t.Setenv("ANTIGRAVITY_API_KEY", tc.antigravityKey)
			subDir := tc.subDir
			if subDir == "" {
				subDir = ".antigravity"
			}
			dir := filepath.Join(home, subDir)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"security":{"auth":{"selectedType":"`+tc.authType+`"}}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if tc.filename != "" {
				if err := os.WriteFile(filepath.Join(dir, tc.filename), []byte(tc.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			check := antigravityAuthenticationCheck()
			if check.Passed != tc.ready {
				t.Fatalf("got %+v, want ready=%v", check, tc.ready)
			}
			if strings.Contains(check.Message, "secret") {
				t.Fatalf("credential leaked: %s", check.Message)
			}
		})
	}
}
