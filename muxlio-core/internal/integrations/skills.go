package integrations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

type SkillResult struct {
	Installed []string
	Preserved []string
}

// InstallSkill copies the complete portable bundle. Changed destination files
// are backed up by content hash; unrelated files are never removed.
func InstallSkill(source, destination string) (SkillResult, error) {
	result := SkillResult{}
	raw, err := os.ReadFile(filepath.Join(source, "SKILL.md"))
	if err != nil {
		return result, err
	}
	metadata, _, err := skillFrontmatter(raw)
	if err != nil {
		return result, err
	}
	var header struct{ Name, Description string }
	if err := yaml.Unmarshal(metadata, &header); err != nil || header.Name != "muxlio" || strings.TrimSpace(header.Description) == "" {
		return result, fmt.Errorf("Muxlio SKILL.md requires valid YAML name: muxlio and description; reinstall the skill bundle")
	}
	type skillFile struct {
		target string
		raw    []byte
	}
	files := []skillFile{}
	// Read and validate the whole tree before replacing any installed files.
	err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("skill bundle contains symlink: %s", path)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("skill bundle contains non-regular file: %s", path)
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		// Backups are user material, never part of the distributed skill.
		if strings.Contains(rel, ".muxlio-backup-") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, rel)
		if err := checkWritePath(target); err != nil {
			return err
		}
		files = append(files, skillFile{target, raw})
		return nil
	})
	if err != nil {
		return result, err
	}
	// Publish the discovery entrypoint after its resources. A failed resource
	// write must not activate new instructions with missing references.
	entrypoint := filepath.Join(destination, "SKILL.md")
	for _, entryLast := range []bool{false, true} {
		for _, file := range files {
			if (file.target == entrypoint) != entryLast {
				continue
			}
			if err := writeSkillFile(file.target, file.raw, &result); err != nil {
				return result, err
			}
			result.Installed = append(result.Installed, file.target)
		}
	}
	return result, nil
}

func skillFrontmatter(raw []byte) ([]byte, []byte, error) {
	normalized := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if strings.HasPrefix(normalized, "---\n") {
		if end := strings.Index(normalized[4:], "\n---\n"); end >= 0 {
			return []byte(normalized[4 : 4+end]), []byte(normalized[4+end+5:]), nil
		}
	}
	return nil, nil, fmt.Errorf("skill or rule requires YAML frontmatter")
}

func writeSkillFile(path string, raw []byte, result *SkillResult) error {
	if err := checkWritePath(path); err != nil {
		return err
	}
	previous, err := os.ReadFile(path)
	if err == nil {
		if bytes.Equal(previous, raw) {
			return nil
		}
		backup := fmt.Sprintf("%s.muxlio-backup-%x", path, sha256.Sum256(previous))
		if err := checkWritePath(backup); err != nil {
			return err
		}
		if existing, err := os.ReadFile(backup); err == nil {
			if !bytes.Equal(existing, previous) {
				return fmt.Errorf("skill backup conflict: %s", backup)
			}
		} else if !os.IsNotExist(err) {
			return err
		} else if err := atomicWrite(backup, previous); err != nil {
			return err
		}
		result.Preserved = append(result.Preserved, backup)
	} else if !os.IsNotExist(err) {
		return err
	}
	return atomicWrite(path, raw)
}

// Cursor introduced Agent Skills in 2.4. Unknown versions use project rules,
// never a guessed filesystem location for global User Rules.
func cursorSupportsSkills() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(ctx, "cursor", "--version").Output()
	if err != nil {
		return false
	}
	version := regexp.MustCompile(`(?m)^\s*(\d+)\.(\d+)\.\d+`).FindStringSubmatch(string(raw))
	if version == nil {
		return false
	}
	major, _ := strconv.Atoi(version[1])
	minor, _ := strconv.Atoi(version[2])
	return major > 2 || major == 2 && minor >= 4
}

func InstallClientSkill(source, base, client, scope string) (SkillResult, error) {
	result := SkillResult{}
	if !filepath.IsAbs(base) || (scope != "user" && scope != "project") {
		return result, fmt.Errorf("skill scope directory must be absolute and scope must be user or project")
	}
	var destination string
	rule := ""
	switch client {
	case "":
		destination = filepath.Join(base, ".agents", "skills", "muxlio")
	case "claude":
		destination = filepath.Join(base, ".claude", "skills", "muxlio")
	case "codex", "antigravity":
		destination = filepath.Join(base, ".agents", "skills", "muxlio")
	case "kiro":
		destination = filepath.Join(base, ".kiro", "skills", "muxlio")
	case "cursor":
		rule = filepath.Join(base, ".cursor", "rules", "muxlio.mdc")
		_, err := os.Lstat(rule)
		if err != nil && !os.IsNotExist(err) {
			return result, err
		}
		// An existing rule keeps its activation semantics, even after upgrades.
		if os.IsNotExist(err) && cursorSupportsSkills() {
			destination = filepath.Join(base, ".cursor", "skills", "muxlio")
			rule = ""
		} else {
			if scope == "user" {
				return result, fmt.Errorf("Cursor user skills require Cursor 2.4+ with cursor --version available; use --scope project for rules or upgrade Cursor")
			}
			destination = filepath.Join(base, ".cursor", "muxlio")
		}
	default:
		return result, fmt.Errorf("unsupported skill client %q", client)
	}
	result, err := InstallSkill(source, destination)
	if err != nil {
		return result, err
	}
	if rule != "" {
		if err := checkWritePath(rule); err != nil {
			return result, err
		}
		previous, err := os.ReadFile(rule)
		if err == nil || os.IsNotExist(err) {
			// Manual invocation avoids attaching orchestration to trivial requests.
			header := []byte("alwaysApply: false")
			if err == nil {
				header, _, err = skillFrontmatter(previous)
				var fields map[string]any
				if err != nil || yaml.Unmarshal(header, &fields) != nil {
					return result, fmt.Errorf("cannot preserve Cursor rule activation metadata: %s", rule)
				}
			}
			entry, err := os.ReadFile(filepath.Join(source, "SKILL.md"))
			if err != nil {
				return result, err
			}
			_, body, err := skillFrontmatter(entry)
			if err != nil {
				return result, err
			}
			raw := []byte("---\n" + string(header) + "\n---\n" + strings.ReplaceAll(string(body), "(references/", "(../muxlio/references/"))
			if err := writeSkillFile(rule, raw, &result); err != nil {
				return result, err
			}
		} else if err != nil {
			return result, err
		}
		result.Installed = append(result.Installed, rule)
	}
	// Retire legacy discovery entrypoints only after a successful installation.
	// Move the whole directory so edited instructions and relative resources stay
	// together, outside client discovery. Never merge arbitrary old instructions.
	legacy := []string{filepath.Join(base, ".skills", "muxlio")}
	if client == "codex" {
		legacy = append(legacy, filepath.Join(base, ".codex", "skills", "muxlio"))
		if scope == "user" && filepath.IsAbs(os.Getenv("CODEX_HOME")) {
			legacy = append(legacy, filepath.Join(os.Getenv("CODEX_HOME"), "skills", "muxlio"))
		}
	}
	for _, path := range legacy {
		if path == destination {
			continue
		}
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return result, err
		}
		if err := checkWritePath(path); err != nil {
			return result, err
		}
		archive := filepath.Join(base, ".muxlio-skill-backups")
		if err := checkWritePath(archive); err != nil {
			return result, err
		}
		if err := os.MkdirAll(archive, 0700); err != nil {
			return result, err
		}
		dir, err := os.MkdirTemp(archive, "legacy-*")
		if err != nil {
			return result, err
		}
		target := filepath.Join(dir, "muxlio")
		if err := os.Rename(path, target); err != nil {
			return result, err
		}
		result.Preserved = append(result.Preserved, target)
	}
	return result, nil
}
