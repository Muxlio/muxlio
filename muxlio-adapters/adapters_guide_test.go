package adapters_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-adapters/contract"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for dir := wd; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "muxlio-docs", "adapters.md")); err == nil {
			return dir
		}
		if filepath.Dir(dir) == dir {
			t.Fatal("repository root not found")
		}
	}
}

func TestAdaptersGuidePublishedInSite(t *testing.T) {
	root := repoRoot(t)
	if _, err := os.Stat(filepath.Join(root, "muxlio-docs", "adapters.md")); err != nil {
		t.Fatalf("adapters guide missing: %v", err)
	}
	mkdocs, err := os.ReadFile(filepath.Join(root, "mkdocs.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mkdocs), "adapters.md") {
		t.Fatal("mkdocs.yml missing adapters.md nav entry")
	}
	readme, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(readme), "Which adapter should I use?") {
		t.Fatal("README.md missing adapter guide link")
	}
}

func TestAdaptersGuideMatchesBundledManifests(t *testing.T) {
	root := repoRoot(t)
	docBytes, err := os.ReadFile(filepath.Join(root, "muxlio-docs", "adapters.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(docBytes)

	bundled := []string{
		"agent-a2a", "agent-acp", "agent-antigravity", "agent-claude",
		"agent-codex", "agent-cursor", "agent-kiro", "command",
	}
	for _, base := range bundled {
		path := filepath.Join(root, "muxlio-adapters", "cmd", base, "adapter.json")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read manifest %s: %v", path, err)
		}
		var manifest contract.AdapterManifest
		if err := json.Unmarshal(raw, &manifest); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		if !strings.Contains(doc, manifest.Executor) {
			t.Fatalf("adapters.md missing executor %q from %s", manifest.Executor, base)
		}
		for cap, supported := range manifest.Capabilities {
			if !supported {
				continue
			}
			marker := "`" + cap + "`"
			if cap == contract.CapabilityProgress {
				marker = "`progress`"
			}
			if !strings.Contains(doc, marker) && !strings.Contains(doc, cap) {
				t.Fatalf("adapters.md missing advertised capability %q from %s", cap, manifest.Executor)
			}
		}
	}

	for _, topic := range []string{
		"ACP v1",
		"A2A 0.3.0",
		"reconcilable",
		"remoteTask",
		"A2A_TOKEN",
		"idempotency",
	} {
		if !strings.Contains(doc, topic) {
			t.Fatalf("adapters.md missing topic %q", topic)
		}
	}
	if !strings.Contains(doc, "Host execution") && !strings.Contains(doc, "allow: true") {
		t.Fatal("adapters.md missing host execution onboarding")
	}
	if !strings.Contains(doc, "consumed") || !strings.Contains(doc, "context-window") {
		t.Fatal("adapters.md must distinguish context occupancy from token totals")
	}
	if !strings.Contains(doc, "unavailable") && !strings.Contains(doc, "Unavailable") {
		t.Fatal("adapters.md must distinguish unavailable accounting")
	}
}
