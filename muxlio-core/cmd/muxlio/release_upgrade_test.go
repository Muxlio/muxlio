package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
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
	for _, path := range []string{obsolete, custom, config} {
		if err := os.WriteFile(path, []byte("preserve existing content\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Exercise the real installer with a stub compiler: this test concerns
	// upgrades and discovery, without rebuilding every Go binary in the suite.
	stubDir := t.TempDir()
	stub := `#!/bin/sh
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
		if _, err := os.Lstat(obsolete); !os.IsNotExist(err) {
			t.Fatalf("obsolete adapter still installed: %v", err)
		}
		for _, path := range []string{custom, config} {
			content, err := os.ReadFile(path)
			if err != nil || string(content) != "preserve existing content\n" {
				t.Fatalf("installer changed %s: %q, %v", path, content, err)
			}
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
