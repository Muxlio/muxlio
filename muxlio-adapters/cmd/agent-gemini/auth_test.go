package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGeminiAuthenticationAvailability(t *testing.T) {
	for _, tc := range []struct {
		name, key, authType, filename, content string
		ready                                  bool
	}{
		{name: "missing"},
		{name: "environment", key: "secret-api-key", ready: true},
		{name: "whitespace key", key: "  "},
		{name: "saved key", authType: "gemini-api-key", filename: "gemini-credentials.json", content: "encrypted-secret", ready: true},
		{name: "cached oauth", authType: "oauth-personal", filename: "oauth_creds.json", content: `{"refresh_token":"secret"}`, ready: true},
		{name: "settings alone", authType: "gemini-api-key"},
		{name: "empty credentials", authType: "gemini-api-key", filename: "gemini-credentials.json"},
		{name: "wrong credential type", authType: "oauth-personal", filename: "gemini-credentials.json", content: "encrypted-secret"},
		{name: "unselected credentials", filename: "gemini-credentials.json", content: "encrypted-secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("GEMINI_CLI_HOME", home)
			t.Setenv("GEMINI_API_KEY", tc.key)
			dir := filepath.Join(home, ".gemini")
			if err := os.Mkdir(dir, 0o700); err != nil {
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
			check := geminiAuthenticationCheck()
			if check.Passed != tc.ready {
				t.Fatalf("got %+v, want ready=%v", check, tc.ready)
			}
			if strings.Contains(check.Message, "secret") {
				t.Fatalf("credential leaked: %s", check.Message)
			}
		})
	}
}
