package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNpmReleaseScriptBuildsRuntimePrefix(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	pkgPath := filepath.Join(repoRoot, "package.json")
	raw, err := os.ReadFile(pkgPath)
	if err != nil {
		t.Fatalf("read package.json: %v", err)
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		t.Fatalf("parse package.json: %v", err)
	}
	release, ok := pkg.Scripts["release"]
	if !ok {
		t.Fatal("package.json missing scripts.release")
	}
	if release != "bash scripts/release.sh" {
		t.Fatalf("scripts.release = %q, want bash scripts/release.sh", release)
	}

	releaseSh := filepath.Join(repoRoot, "scripts", "release.sh")
	script, err := os.ReadFile(releaseSh)
	if err != nil {
		t.Fatalf("read scripts/release.sh: %v", err)
	}
	body := string(script)
	for _, want := range []string{
		`runtime_dir="${MUXLIO_HOME:-${HOME}/.muxlio}"`,
		`scripts/build-release.sh`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("scripts/release.sh missing %q", want)
		}
	}

	readmePath := filepath.Join(repoRoot, "muxlio-docs", "architecture", "developer-reference.md")
	readme, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	doc := string(readme)
	if !strings.Contains(doc, "npm run release") {
		t.Fatal("developer reference missing npm run release")
	}
}
