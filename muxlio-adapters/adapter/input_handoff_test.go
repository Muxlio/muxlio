package adapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestComposePromptMaterializesOversizedInput(t *testing.T) {
	dir := t.TempDir()
	large := strings.Repeat("x", MaxInlineInputBytes+1)
	req := &Request{
		Task:    "Use the notes.",
		WorkDir: dir,
		Inputs:  map[string]any{"notes": large},
	}
	prompt, err := ComposePrompt(req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prompt, large) {
		t.Fatal("oversized input was inlined into the prompt")
	}
	if !strings.Contains(prompt, "NOT inlined") || !strings.Contains(prompt, filepath.Join(dir, "handoff")+string(filepath.Separator)+"notes-") {
		t.Fatalf("expected handoff file reference: %s", prompt)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "handoff"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("handoff entries: %v %v", entries, err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "handoff", entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != large {
		t.Fatalf("materialized file incomplete: got %d want %d", len(body), len(large))
	}
	info, err := entries[0].Info()
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected 0600 perms, got %v", info.Mode().Perm())
	}
}

func TestComposePromptMaterializesOversizedJSON(t *testing.T) {
	dir := t.TempDir()
	payload := map[string]any{"blob": strings.Repeat("y", MaxInlineInputBytes)}
	req := &Request{Task: "Inspect", WorkDir: dir, Inputs: map[string]any{"payload": payload}}
	prompt, err := ComposePrompt(req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, filepath.Join(dir, "handoff")) || !strings.Contains(prompt, "application/json") {
		t.Fatalf("expected json handoff: %s", prompt)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "handoff"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("handoff entries: %v %v", entries, err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "handoff", entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"blob"`) || !strings.Contains(string(body), strings.Repeat("y", 32)) {
		t.Fatalf("json file incomplete: %s", body[:min(80, len(body))])
	}
}

func TestComposePromptHandoffDoesNotOverwriteInputsArtifacts(t *testing.T) {
	dir := t.TempDir()
	artifactDir := filepath.Join(dir, "inputs")
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(artifactDir, "notes.txt")
	original := "artifact-bytes-must-survive"
	if err := os.WriteFile(artifactPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	large := strings.Repeat("x", MaxInlineInputBytes+1)
	req := &Request{
		Task: "Use both", WorkDir: dir,
		Inputs: map[string]any{
			"notes": large,
			"doc":   map[string]any{"path": artifactPath, "mime": "text/plain"},
		},
	}
	if _, err := ComposePrompt(req); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != original {
		t.Fatalf("artifact overwritten: %q", body)
	}
}

func TestComposePromptRejectsInaccessibleArtifactPath(t *testing.T) {
	workDir, outside := t.TempDir(), t.TempDir()
	secret := filepath.Join(outside, "secret.md")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	req := &Request{
		Task: "Read", WorkDir: workDir,
		Inputs: map[string]any{"notes": map[string]any{"path": secret, "mime": "text/markdown"}},
	}
	if _, err := ComposePrompt(req); err == nil {
		t.Fatal("expected inaccessible path to fail before invoke")
	}
}

func TestComposeRemotePromptRejectsOversizedWithoutFabricatingPaths(t *testing.T) {
	large := strings.Repeat("z", MaxInlineInputBytes+1)
	req := &Request{Task: "Remote", Inputs: map[string]any{"notes": large}}
	prompt, err := ComposeRemotePrompt(req)
	if err == nil {
		t.Fatalf("expected fileless oversized failure, got prompt %q", prompt)
	}
	if strings.Contains(err.Error(), "/handoff/") || strings.Contains(err.Error(), "/inputs/") {
		t.Fatalf("fileless error must not invent local paths: %v", err)
	}
}

func TestComposePromptSortedInputNames(t *testing.T) {
	dir := t.TempDir()
	req := &Request{
		Task: "Order", WorkDir: dir,
		Inputs: map[string]any{"zeta": "z", "alpha": "a", "mu": "m"},
	}
	prompt, err := ComposePrompt(req)
	if err != nil {
		t.Fatal(err)
	}
	alpha := strings.Index(prompt, "--- alpha ---")
	mu := strings.Index(prompt, "--- mu ---")
	zeta := strings.Index(prompt, "--- zeta ---")
	if !(alpha < mu && mu < zeta) {
		t.Fatalf("inputs not sorted: %s", prompt)
	}
}

func TestComposePromptOversizedFeatureRemainsReferenced(t *testing.T) {
	dir := t.TempDir()
	large := strings.Repeat("f", MaxInlineInputBytes+1)
	req := &Request{
		Task: "Implement", WorkDir: dir,
		Inputs: map[string]any{"feature": map[string]any{"feature": large, "id": "1"}},
	}
	prompt, err := ComposePrompt(req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prompt, large) {
		t.Fatal("oversized feature was inlined")
	}
	if !strings.Contains(prompt, "NOT inlined") || !strings.Contains(prompt, filepath.Join(dir, "handoff")+string(filepath.Separator)+"feature-") {
		t.Fatalf("expected feature handoff reference: %s", prompt)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "handoff"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("handoff entries: %v %v", entries, err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "handoff", entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), large) {
		t.Fatal("feature file incomplete")
	}
}

func TestComposePromptBinaryAsFileRef(t *testing.T) {
	dir := t.TempDir()
	raw := string([]byte{0x00, 0xff, 0xfe}) + strings.Repeat("b", MaxInlineInputBytes)
	req := &Request{Task: "Bin", WorkDir: dir, Inputs: map[string]any{"blob": raw}}
	prompt, err := ComposePrompt(req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "NOT inlined") || !strings.Contains(prompt, filepath.Join(dir, "handoff")) {
		t.Fatalf("expected binary handoff: %s", prompt)
	}
}
