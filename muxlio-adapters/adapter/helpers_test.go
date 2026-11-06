package adapter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestParseTokenUsagePreservesUnavailableAndReportedZero(t *testing.T) {
	for _, raw := range []string{
		`{"input_tokens":null,"output_tokens":null,"cached_input_tokens":null}`,
		`{"input_tokens":-1,"output_tokens":1.5,"cached_input_tokens":"unknown"}`,
	} {
		if got := ParseTokenUsage(json.RawMessage(raw)); got != nil {
			t.Fatalf("invalid or unknown counts became usage: %+v", got)
		}
	}
	if got := ParseTokenUsage(nil); got != nil {
		t.Fatalf("missing usage = %+v, want nil", got)
	}
	if got := ParseTokenUsage(json.RawMessage(`{"other":1}`)); got != nil {
		t.Fatalf("unrecognized usage = %+v, want nil", got)
	}
	got := ParseTokenUsage(json.RawMessage(`{"input_tokens":0,"output_tokens":0,"cached_input_tokens":0}`))
	if got == nil || got.Input != 0 || got.Output != 0 || got.Cached != 0 {
		t.Fatalf("reported zero = %+v", got)
	}
}

func TestParseTokenUsageProviderSummaryFields(t *testing.T) {
	for _, raw := range []string{
		`{"uncachedInputTokens":100,"outputTokens":40,"cacheReadInputTokens":12}`,
		`{"input_tokens":100,"output_tokens":40,"cached":12,"input":88}`,
	} {
		got := ParseTokenUsage(json.RawMessage(raw))
		if got == nil || got.Input != 100 || got.Output != 40 || got.Cached != 12 {
			t.Fatalf("summary %s parsed as %+v", raw, got)
		}
	}
}

func TestParseTokenUsageNormalizesProviderKeys(t *testing.T) {
	got := ParseTokenUsage(json.RawMessage(`{"prompt_token_count":100,"candidates_token_count":40,"cached_content_token_count":12}`))
	if got == nil || got.Input != 100 || got.Output != 40 || got.Cached != 12 {
		t.Fatalf("normalized usage = %+v", got)
	}
}

func TestParseTokenUsageHandlesCamelCaseKeys(t *testing.T) {
	got := ParseTokenUsage(json.RawMessage(`{"inputTokens":100,"outputTokens":40,"cachedTokens":12}`))
	if got == nil || got.Input != 100 || got.Output != 40 || got.Cached != 12 {
		t.Fatalf("camelCase usage = %+v", got)
	}
}

func TestParseTokenUsageHandlesCursorMetadataCamelCase(t *testing.T) {
	got := ParseTokenUsage(json.RawMessage(`{"inputTokens":100,"outputTokens":40,"cacheReadTokens":12}`))
	if got == nil || got.Input != 100 || got.Output != 40 || got.Cached != 12 {
		t.Fatalf("Cursor metadata usage = %+v", got)
	}
}

func TestParseTokenUsageHandlesGeminiMetadataCamelCase(t *testing.T) {
	got := ParseTokenUsage(json.RawMessage(`{"promptTokenCount":100,"candidatesTokenCount":40,"cachedContentTokenCount":12}`))
	if got == nil || got.Input != 100 || got.Output != 40 || got.Cached != 12 {
		t.Fatalf("gemini metadata usage = %+v", got)
	}
}

// ---- WorkingDir -------------------------------------------------------

func TestWorkingDirFallsBackToWorkspaceWhenNoInput(t *testing.T) {
	dir := t.TempDir()
	req := &Request{Workspace: dir, Inputs: map[string]any{}}
	got, err := WorkingDir(req)
	if err != nil {
		t.Fatal(err)
	}
	// EvalSymlinks may change the path; check it exists and is a directory.
	if info, err := os.Stat(got); err != nil || !info.IsDir() {
		t.Fatalf("expected existing dir, got %q err=%v", got, err)
	}
}

func TestWorkingDirUsesWorkspaceInputString(t *testing.T) {
	dir := t.TempDir()
	workspace := t.TempDir()
	req := &Request{
		Workspace: dir,
		Inputs:    map[string]any{"workspace": workspace},
	}
	got, err := WorkingDir(req)
	if err != nil {
		t.Fatal(err)
	}
	resolved, _ := filepath.EvalSymlinks(workspace)
	if got != resolved {
		t.Errorf("got %q, want %q", got, resolved)
	}
}

func TestWorkingDirUsesWorkspaceInputMap(t *testing.T) {
	dir := t.TempDir()
	workspace := t.TempDir()
	req := &Request{
		Workspace: dir,
		Inputs: map[string]any{
			"workspace": map[string]any{"data": workspace},
		},
	}
	got, err := WorkingDir(req)
	if err != nil {
		t.Fatal(err)
	}
	resolved, _ := filepath.EvalSymlinks(workspace)
	if got != resolved {
		t.Errorf("got %q, want %q", got, resolved)
	}
}

func TestWorkingDirIgnoresNonAbsoluteWorkspaceInput(t *testing.T) {
	dir := t.TempDir()
	req := &Request{
		Workspace: dir,
		Inputs:    map[string]any{"workspace": "relative/path"},
	}
	got, err := WorkingDir(req)
	if err != nil {
		t.Fatal(err)
	}
	resolved, _ := filepath.EvalSymlinks(dir)
	if got != resolved {
		t.Errorf("expected fallback to workspace %q, got %q", resolved, got)
	}
}

// ---- CollectOutputs ---------------------------------------------------

func TestCollectOutputsScalarJSON(t *testing.T) {
	dir := t.TempDir()
	// Write a JSON boolean (the "passed" output from verify).
	if err := os.WriteFile(filepath.Join(dir, "passed"), []byte("true"), 0o644); err != nil {
		t.Fatal(err)
	}
	req := &Request{
		WorkDir: dir,
		Outputs: map[string]OutputDecl{
			"passed": {Mime: "application/json"}, // scalar: no Path
		},
	}
	outputs, missing, _ := CollectOutputs(req)
	if len(missing) != 0 {
		t.Fatalf("unexpected missing: %v", missing)
	}
	if outputs["passed"] != true {
		t.Errorf("passed = %v, want true", outputs["passed"])
	}
}

func TestCollectOutputsScalarString(t *testing.T) {
	dir := t.TempDir()
	// Write a JSON-encoded string (the "path" output from prepare).
	if err := os.WriteFile(filepath.Join(dir, "path"), []byte(`"/srv/muxlio"`), 0o644); err != nil {
		t.Fatal(err)
	}
	req := &Request{
		WorkDir: dir,
		Outputs: map[string]OutputDecl{
			"path": {Mime: "application/json"}, // scalar: no Path
		},
	}
	outputs, missing, _ := CollectOutputs(req)
	if len(missing) != 0 {
		t.Fatalf("unexpected missing: %v", missing)
	}
	if outputs["path"] != "/srv/muxlio" {
		t.Errorf("path = %v, want /srv/muxlio", outputs["path"])
	}
}

func TestCollectOutputsArtifactReturnedAsMap(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("# Notes\nDone."), 0o644); err != nil {
		t.Fatal(err)
	}
	req := &Request{
		WorkDir: dir,
		Outputs: map[string]OutputDecl{
			"notes": {Mime: "text/markdown", Path: "notes.md"}, // artifact: has Path
		},
	}
	outputs, missing, _ := CollectOutputs(req)
	if len(missing) != 0 {
		t.Fatalf("unexpected missing: %v", missing)
	}
	m, ok := outputs["notes"].(map[string]any)
	if !ok {
		t.Fatalf("notes output is not a map: %T", outputs["notes"])
	}
	if m["data"] != "# Notes\nDone." {
		t.Errorf("notes data = %q", m["data"])
	}
}

func TestCollectOutputsMissingFile(t *testing.T) {
	dir := t.TempDir()
	req := &Request{
		WorkDir: dir,
		Outputs: map[string]OutputDecl{
			"report": {Mime: "text/markdown", Path: "report.md"},
		},
	}
	_, missing, _ := CollectOutputs(req)
	if len(missing) != 1 || missing[0] != "report" {
		t.Errorf("expected [report] missing, got %v", missing)
	}
}

// ---- InputContent -----------------------------------------------------

func TestInputContentString(t *testing.T) {
	content, ok := InputContent("hello world")
	if !ok || content != "hello world" {
		t.Errorf("got %q %v", content, ok)
	}
}

func TestInputContentMapData(t *testing.T) {
	content, ok := InputContent(map[string]any{"data": "inline text", "mime": "text/plain"})
	if !ok || content != "inline text" {
		t.Errorf("got %q %v", content, ok)
	}
}

func TestInputContentMapPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "review.md")
	if err := os.WriteFile(path, []byte("looks good"), 0o644); err != nil {
		t.Fatal(err)
	}
	content, ok := InputContent(map[string]any{"path": path, "mime": "text/markdown"})
	if !ok || content != "looks good" {
		t.Errorf("got %q %v", content, ok)
	}
}

func TestInputContentEmpty(t *testing.T) {
	if _, ok := InputContent(""); ok {
		t.Error("empty string should return false")
	}
	if _, ok := InputContent(map[string]any{}); ok {
		t.Error("empty map should return false")
	}
	if _, ok := InputContent(42); ok {
		t.Error("int should return false")
	}
}

// ---- ComposePrompt ----------------------------------------------------

func TestComposePromptContainsTask(t *testing.T) {
	req := &Request{
		Task:    "Do the thing.",
		WorkDir: "/tmp/work",
	}
	p := ComposePrompt(req)
	if !contains(p, "Do the thing.") {
		t.Error("prompt missing task text")
	}
	if !contains(p, "/tmp/work") {
		t.Error("prompt missing workDir")
	}
}

func TestComposePromptExpandsFeature(t *testing.T) {
	req := &Request{
		Task:    "Implement feature.",
		WorkDir: "/tmp/work",
		Inputs: map[string]any{
			"feature": map[string]any{
				"id":      "001",
				"feature": "Add pause/resume",
				"context": "Persist loop state.",
			},
		},
	}
	p := ComposePrompt(req)
	if !contains(p, "Add pause/resume") {
		t.Error("prompt missing feature description")
	}
	if !contains(p, "Persist loop state.") {
		t.Error("prompt missing feature context")
	}
}

func TestComposePromptInlinesArtifactInput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(path, []byte("Implementation notes here"), 0o644); err != nil {
		t.Fatal(err)
	}
	req := &Request{
		Task:    "Review the implementation.",
		WorkDir: "/tmp/work",
		Inputs: map[string]any{
			"notes": map[string]any{"path": path, "mime": "text/markdown"},
		},
	}
	p := ComposePrompt(req)
	if !contains(p, "Implementation notes here") {
		t.Error("prompt missing inlined notes content")
	}
}

func TestComposePromptListsOutputFiles(t *testing.T) {
	req := &Request{
		Task:    "Do work.",
		WorkDir: "/tmp/work",
		Outputs: map[string]OutputDecl{
			"notes":  {Mime: "text/markdown", Path: "notes.md"}, // artifact
			"passed": {Mime: "application/json"},                // scalar
		},
	}
	p := ComposePrompt(req)
	// Artifact output listed with full path
	if !contains(p, "/tmp/work/notes.md") {
		t.Error("prompt missing artifact output path")
	}
	// Scalar output listed under its port name with JSON guidance
	if !contains(p, "/tmp/work/passed") {
		t.Error("prompt missing scalar output path")
	}
	if !contains(p, "JSON value") {
		t.Error("prompt missing JSON value guidance for scalar output")
	}
	// Scalar and artifact sections are separated
	if !contains(p, "Scalar outputs") {
		t.Error("prompt missing Scalar outputs section")
	}
	if !contains(p, "File artifact outputs") {
		t.Error("prompt missing File artifact outputs section")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}

func TestCollectOutputsRejectsProseAsJSONAndPreservesArtifacts(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"approval", "message", "notes.md"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("looks good"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	outputs, missing, invalid := CollectOutputs(&Request{WorkDir: dir, Outputs: map[string]OutputDecl{
		"approval": {Schema: json.RawMessage(`{"type":"boolean"}`)},
		"message":  {Schema: json.RawMessage(`{"type":"string"}`)},
		"absent":   {Schema: json.RawMessage(`{"type":"boolean"}`)},
		"notes":    {Path: "notes.md", Mime: "text/markdown"},
	}})
	if len(missing) != 0 || len(invalid) != 3 || invalid["approval"] == "" || invalid["message"] == "" || invalid["absent"] == "" {
		t.Fatalf("missing=%v invalid=%v", missing, invalid)
	}
	if outputs["approval"] != "looks good" || outputs["notes"].(map[string]any)["data"] != "looks good" {
		t.Fatalf("lost inspectable content: %v", outputs)
	}
}
