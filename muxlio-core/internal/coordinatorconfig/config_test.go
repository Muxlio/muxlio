package coordinatorconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestExplicitCredentialReferencesAndTransport(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(filepath.Join(dir, "admin-token"), []byte(" admin\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, endpoint, ref, target, override, want string
		fail                                        bool
	}{
		{name: "local reference", endpoint: "http://127.0.0.1:7600", ref: "root", want: "admin"},
		{name: "IPv6 reference", endpoint: "http://[::1]:7600", ref: "root", want: "admin"},
		{name: "remote no inheritance", endpoint: "https://remote.example"},
		{name: "local no inheritance", endpoint: "http://127.0.0.1:7600"},
		{name: "literal ignores profile", endpoint: "http://127.0.0.1:7600", ref: "root", target: "http://127.0.0.1:7600"},
		{name: "remote root forbidden", endpoint: "https://remote.example", ref: "root", fail: true},
		{name: "remote root override forbidden", endpoint: "https://remote.example", ref: "root", override: "explicit", fail: true},
		{name: "unknown reference", endpoint: "http://localhost:7600", ref: "typo", fail: true},
		{name: "insecure remote", endpoint: "http://remote.example", override: "explicit", fail: true},
		{name: "explicit override", endpoint: "http://localhost:7600", ref: "root", override: "override", want: "override"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := map[string]any{"defaultServer": "selected", "tokenFile": "admin-token", "servers": map[string]any{"selected": map[string]string{"url": tc.endpoint, "credentialRef": tc.ref}}}
			raw, _ := json.Marshal(cfg)
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			_, token, err := ResolveFile(path, tc.target, "", tc.override)
			if (err != nil) != tc.fail || token != tc.want {
				t.Fatalf("token matches=%t error=%v", token == tc.want, err)
			}
		})
	}
}
