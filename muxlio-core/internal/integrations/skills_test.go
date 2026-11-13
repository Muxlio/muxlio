package integrations

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func skillSource(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs("../../../muxlio-skills")
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func cursorVersionFixture(t *testing.T, version string) {
	t.Helper()
	bin := t.TempDir()
	writeFixture(t, filepath.Join(bin, "cursor"), []byte("#!/bin/sh\nprintf '%s\\n' '"+version+"'\n"))
	if err := os.Chmod(filepath.Join(bin, "cursor"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
}

func TestClientSkillDiscoveryAndReferences(t *testing.T) {
	cursorVersionFixture(t, "2.4.1")
	for _, client := range Clients {
		for _, scope := range []string{"project", "user"} {
			t.Run(client+"/"+scope, func(t *testing.T) {
				// Deliberately unrelated to the Muxlio checkout.
				base := t.TempDir()
				result, err := InstallClientSkill(skillSource(t), base, client, scope)
				if err != nil {
					t.Fatal(err)
				}
				dir := map[string]string{"claude": ".claude", "codex": ".agents", "kiro": ".kiro", "cursor": ".cursor"}[client]
				entry := filepath.Join(base, dir, "skills", "muxlio", "SKILL.md")
				raw, err := os.ReadFile(entry)
				if err != nil {
					t.Fatal(err)
				}
				metadata, _, err := skillFrontmatter(raw)
				var header struct{ Name, Description string }
				if err != nil || yaml.Unmarshal(metadata, &header) != nil || header.Name != "muxlio" || header.Description == "" {
					t.Fatalf("unreadable discovery metadata: %s", raw)
				}
				for _, ref := range []string{"authoring.md", "operations.md", "security.md"} {
					if _, err := os.ReadFile(filepath.Join(filepath.Dir(entry), "references", ref)); err != nil {
						t.Fatal(err)
					}
				}
				if len(result.Preserved) != 0 {
					t.Fatal(result)
				}
				again, err := InstallClientSkill(skillSource(t), base, client, scope)
				if err != nil || len(again.Preserved) != 0 {
					t.Fatalf("repeat installation: %+v %v", again, err)
				}
			})
		}
	}
}

func TestSkillMigrationPreservesEditedFilesAndLegacyResources(t *testing.T) {
	base := t.TempDir()
	t.Setenv("CODEX_HOME", t.TempDir())
	legacy := []string{filepath.Join(base, ".skills", "muxlio"), filepath.Join(base, ".codex", "skills", "muxlio"), filepath.Join(os.Getenv("CODEX_HOME"), "skills", "muxlio")}
	for _, dir := range legacy {
		writeFixture(t, filepath.Join(dir, "SKILL.md"), []byte("my old instructions"))
		writeFixture(t, filepath.Join(dir, "references", "custom.md"), []byte("my old resource"))
	}
	target := filepath.Join(base, ".agents", "skills", "muxlio")
	writeFixture(t, filepath.Join(target, "SKILL.md"), []byte("my edited instructions"))
	writeFixture(t, filepath.Join(target, "custom.txt"), []byte("keep"))
	result, err := InstallClientSkill(skillSource(t), base, "codex", "user")
	if err != nil || len(result.Preserved) != 4 {
		t.Fatalf("migration: %+v %v", result, err)
	}
	for _, path := range result.Preserved {
		if strings.Contains(path, ".muxlio-backup-") {
			raw, err := os.ReadFile(path)
			if err != nil || string(raw) != "my edited instructions" {
				t.Fatalf("edited file lost: %s %v", raw, err)
			}
		} else {
			for _, name := range []string{"SKILL.md", "references/custom.md"} {
				if _, err := os.ReadFile(filepath.Join(path, name)); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	for _, path := range legacy {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("legacy skill still discoverable: %s", path)
		}
	}
	if raw, err := os.ReadFile(filepath.Join(target, "custom.txt")); err != nil || string(raw) != "keep" {
		t.Fatal("unrelated material lost")
	}
	again, err := InstallClientSkill(skillSource(t), base, "codex", "user")
	if err != nil || len(again.Preserved) != 0 {
		t.Fatalf("repeat migration: %+v %v", again, err)
	}
}

func TestCursorRulesPreserveActivation(t *testing.T) {
	for _, header := range []string{"alwaysApply: false", "description: Muxlio workflows\nglobs: workflows/**,*.plan.json", "alwaysApply: true", "description: Custom Muxlio guidance\nalwaysApply: false"} {
		t.Run(header, func(t *testing.T) {
			cursorVersionFixture(t, "2.4.1")
			base := t.TempDir()
			rule := filepath.Join(base, ".cursor", "rules", "muxlio.mdc")
			before := []byte("---\n" + header + "\n---\nUser edits\n")
			writeFixture(t, rule, before)
			result, err := InstallClientSkill(skillSource(t), base, "cursor", "project")
			if err != nil || len(result.Preserved) != 1 {
				t.Fatalf("rule migration: %+v %v", result, err)
			}
			raw, _ := os.ReadFile(rule)
			metadata, body, err := skillFrontmatter(raw)
			if err != nil || string(metadata) != header || bytes.Contains(body, []byte("\n---\n")) {
				t.Fatalf("changed activation or nested frontmatter: %s", raw)
			}
			if !bytes.Contains(body, []byte("../muxlio/references/operations.md")) {
				t.Fatal("rule references are not relative to rule location")
			}
			backup, _ := os.ReadFile(result.Preserved[0])
			if !bytes.Equal(backup, before) {
				t.Fatal("lost custom rule body")
			}
			if _, err := os.Stat(filepath.Join(base, ".cursor", "skills")); !os.IsNotExist(err) {
				t.Fatal("installed competing skill beside rule")
			}
		})
	}
}

func TestCursorVersionFallbackAndUserScope(t *testing.T) {
	for _, version := range []string{"2.3.9", "2.4.0", "3.0.0", "unknown", "0.49.6"} {
		t.Run(version, func(t *testing.T) {
			cursorVersionFixture(t, version)
			modern := version == "2.4.0" || version == "3.0.0"
			base := t.TempDir()
			if _, err := InstallClientSkill(skillSource(t), base, "cursor", "project"); err != nil {
				t.Fatal(err)
			}
			if !modern {
				raw, err := os.ReadFile(filepath.Join(base, ".cursor", "rules", "muxlio.mdc"))
				if err != nil || !bytes.HasPrefix(raw, []byte("---\nalwaysApply: false\n---\n")) {
					t.Fatalf("expected manual rule: %s %v", raw, err)
				}
			}
			_, err := InstallClientSkill(skillSource(t), t.TempDir(), "cursor", "user")
			if (err == nil) != modern {
				t.Fatalf("unsupported user scope accepted or modern scope rejected: %v", err)
			}
		})
	}
}

func TestSkillRejectsInvalidMetadataAndSymlinks(t *testing.T) {
	for _, raw := range []string{"# no frontmatter", "---\nname: muxlio\n---\nbody", "---\nname: other\ndescription: test\n---\nbody", "---\nname: [\n---\nbody"} {
		source, target := t.TempDir(), t.TempDir()
		writeFixture(t, filepath.Join(source, "SKILL.md"), []byte(raw))
		if _, err := InstallSkill(source, target); err == nil {
			t.Fatalf("accepted invalid skill %q", raw)
		}
	}
	base, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(base, ".kiro")); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallClientSkill(skillSource(t), base, "kiro", "project"); err == nil {
		t.Fatal("wrote through symlink")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatal("modified symlink target")
	}
}

func TestFailedSkillUpgradePreservesEntrypoint(t *testing.T) {
	for _, failure := range []string{"source symlink", "destination symlink", "resource write failure"} {
		t.Run(failure, func(t *testing.T) {
			source, target, outside := t.TempDir(), t.TempDir(), t.TempDir()
			if _, err := InstallSkill(skillSource(t), source); err != nil {
				t.Fatal(err)
			}
			old := []byte("Existing user instructions\n")
			writeFixture(t, filepath.Join(target, "SKILL.md"), old)
			switch failure {
			case "source symlink":
				// Walk order previously replaced SKILL.md before rejecting this.
				if err := os.Symlink(outside, filepath.Join(source, "z-link")); err != nil {
					t.Fatal(err)
				}
			case "destination symlink":
				if err := os.Symlink(outside, filepath.Join(target, "references")); err != nil {
					t.Fatal(err)
				}
			case "resource write failure":
				writeFixture(t, filepath.Join(target, "references"), []byte("user file blocking directory creation"))
			}
			if _, err := InstallSkill(source, target); err == nil {
				t.Fatal("invalid upgrade succeeded")
			}
			got, err := os.ReadFile(filepath.Join(target, "SKILL.md"))
			if err != nil || !bytes.Equal(got, old) {
				t.Fatalf("failed upgrade replaced entrypoint: %s %v", got, err)
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 0 {
				t.Fatal("failed upgrade modified symlink target")
			}
		})
	}
}
