package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCanvasMCPTools(t *testing.T) {
	s := &server{}

	// list_canvas_components
	listRes := s.dispatchTool("list_canvas_components", []byte(`{}`))
	if listRes.IsError {
		t.Fatalf("list_canvas_components error: %+v", listRes)
	}
	if len(listRes.Content) == 0 || !strings.Contains(listRes.Content[0].Text, "table") {
		t.Fatalf("expected list_canvas_components to contain 'table', got %s", listRes.Content[0].Text)
	}

	// get_canvas_component
	getRes := s.dispatchTool("get_canvas_component", []byte(`{"type": "table"}`))
	if getRes.IsError {
		t.Fatalf("get_canvas_component error: %+v", getRes)
	}
	if !strings.Contains(getRes.Content[0].Text, "Per-Executor Performance") {
		t.Errorf("expected table example in get_canvas_component, got %s", getRes.Content[0].Text)
	}

	// get_canvas_component unknown
	getErr := s.dispatchTool("get_canvas_component", []byte(`{"type": "nonexistent"}`))
	if !getErr.IsError {
		t.Errorf("expected error for nonexistent component, got success")
	}

	// validate_canvas with inline content
	validContent := `{
		"version": 1,
		"title": "MCP Validated",
		"content": {
			"type": "heading",
			"id": "h1",
			"level": 1,
			"text": "Hello MCP"
		}
	}`
	valRes := s.dispatchTool("validate_canvas", []byte(`{"content": `+validContent+`}`))
	if valRes.IsError {
		t.Fatalf("validate_canvas failed: %+v", valRes)
	}
	if !strings.Contains(valRes.Content[0].Text, `"valid": true`) {
		t.Errorf("expected valid: true, got %s", valRes.Content[0].Text)
	}

	// validate_canvas with file path
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "test.canvas.json")
	if err := os.WriteFile(filePath, []byte(validContent), 0644); err != nil {
		t.Fatal(err)
	}
	valFileRes := s.dispatchTool("validate_canvas", []byte(`{"path": `+string(mustJSON(filePath))+`}`))
	if valFileRes.IsError {
		t.Fatalf("validate_canvas file failed: %+v", valFileRes)
	}
	if !strings.Contains(valFileRes.Content[0].Text, `"valid": true`) {
		t.Errorf("expected valid: true, got %s", valFileRes.Content[0].Text)
	}

	// validate_canvas invalid
	invalidContent := `{"version": 10}`
	valInvalid := s.dispatchTool("validate_canvas", []byte(`{"content": `+invalidContent+`}`))
	if !valInvalid.IsError {
		t.Errorf("expected validate_canvas invalid to return error")
	}

	if res := s.dispatchTool("validate_canvas", []byte(`{"path": `+string(mustJSON(tmpDir))+`}`)); !res.IsError || !strings.Contains(res.Content[0].Text, "regular file") {
		t.Fatalf("directory path accepted: %+v", res)
	}
	large := filepath.Join(tmpDir, "large.canvas.json")
	if err := os.WriteFile(large, []byte(strings.Repeat(" ", 3<<20)), 0644); err != nil {
		t.Fatal(err)
	}
	if res := s.dispatchTool("validate_canvas", []byte(`{"path": `+string(mustJSON(large))+`}`)); !res.IsError || !strings.Contains(res.Content[0].Text, "exceeds maximum") {
		t.Fatalf("oversize file not rejected by size: %+v", res)
	}
}

func TestReadOnlyToolAnnotations(t *testing.T) {
	offline := map[string]bool{"list_canvas_components": true, "get_canvas_component": true, "validate_canvas": true}
	readOnly := map[string]bool{"list_workflow_versions": true, "preflight_run": true}
	for _, tool := range tools {
		if !offline[tool.Name] && !readOnly[tool.Name] {
			continue
		}
		a := tool.Annotations
		if a == nil || !a.ReadOnly || a.Destructive || !a.Idempotent || a.OpenWorld == offline[tool.Name] {
			t.Errorf("%s annotations: %+v", tool.Name, a)
		}
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
