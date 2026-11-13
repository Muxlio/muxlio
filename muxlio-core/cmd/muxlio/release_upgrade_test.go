package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestReleaseUpgradeRemovesObsoleteAdapter(t *testing.T) {
	repoRoot, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	prefix := filepath.Join(t.TempDir(), "runtime with spaces")
	adapters := filepath.Join(prefix, "libexec", "muxlio", "adapters")
	if err := os.MkdirAll(adapters, 0o755); err != nil {
		t.Fatal(err)
	}
	obsolete := filepath.Join(adapters, "command-go-test")
	custom := filepath.Join(adapters, "custom-tool")
	config := filepath.Join(prefix, "config.json")
	for _, path := range []string{obsolete, custom} {
		if err := os.WriteFile(path, []byte("preserve existing content\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(config, []byte(`{"capabilities":["agent/antigravity","custom/tool"],"executors":{"agent/antigravity":{"allow":false}},"token":"preserve-existing-token","custom":{"keep":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	// Exercise the real installer with a stub compiler: this test concerns
	// upgrades and discovery, without rebuilding every Go binary in the suite.
	stubDir := t.TempDir()
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MUXLIO_TEST_GO", goBinary)
	stub := `#!/bin/sh
if [ "$1" = run ]; then exec "$MUXLIO_TEST_GO" "$@"; fi
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then
    shift
    printf '#!/bin/sh\nexit 0\n' > "$1"
    chmod +x "$1"
    exit 0
  fi
  shift
done
exit 1
`
	if err := os.WriteFile(filepath.Join(stubDir, "go"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for i := 0; i < 2; i++ {
		cmd := exec.Command("bash", filepath.Join(repoRoot, "scripts", "build-release.sh"), prefix)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("install %d: %v\n%s", i, err, output)
		}
		for _, name := range []string{"echo", "task"} {
			installed, err := os.ReadFile(filepath.Join(prefix, "share", "muxlio", "templates", name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			source, err := os.ReadFile(filepath.Join(repoRoot, "muxlio-core", "internal", "authoring", "templates", name+".json"))
			if err != nil || string(installed) != string(source) {
				t.Fatalf("installed template %s differs: %v", name, err)
			}
		}
		if _, err := os.Lstat(obsolete); !os.IsNotExist(err) {
			t.Fatalf("obsolete adapter still installed: %v", err)
		}
		executor := workers.NewScriptExecutor(adapters, t.TempDir())
		if allowed, err := executor.CanExecute("command"); err != nil || allowed {
			t.Fatalf("release did not install the host-authorization descriptor: allowed=%v error=%v", allowed, err)
		}
		for _, path := range []string{custom} {
			content, err := os.ReadFile(path)
			if err != nil || string(content) != "preserve existing content\n" {
				t.Fatalf("installer changed %s: %q, %v", path, content, err)
			}
		}
		raw, err := os.ReadFile(config)
		if err != nil {
			t.Fatal(err)
		}
		var migrated struct {
			Capabilities []string `json:"capabilities"`
			Executors    map[string]struct {
				Allow bool `json:"allow"`
			} `json:"executors"`
			Token  string          `json:"token"`
			Custom map[string]bool `json:"custom"`
		}
		if err := json.Unmarshal(raw, &migrated); err != nil {
			t.Fatal(err)
		}
		if len(migrated.Capabilities) != 2 || migrated.Capabilities[0] != "agent/antigravity" || migrated.Capabilities[1] != "custom/tool" || migrated.Token != "preserve-existing-token" || !migrated.Custom["keep"] {
			t.Fatalf("upgrade lost configuration: %+v", migrated)
		}
		if permission, ok := migrated.Executors["agent/antigravity"]; !ok || permission.Allow {
			t.Fatal("upgrade lost executor deny permission")
		}
		capabilities, err := discoverCapabilities(adapters)
		if err != nil {
			t.Fatal(err)
		}
		found := map[string]bool{}
		for _, capability := range capabilities {
			found[capability.Name] = true
		}
		for _, name := range append(append([]string(nil), BundledExecutorCapabilities...), "custom/tool") {
			if !found[name] {
				t.Errorf("installed adapter not discoverable: %s", name)
			}
		}
		if found["command/go-test"] {
			t.Fatal("worker still advertises the obsolete adapter")
		}
	}
}
