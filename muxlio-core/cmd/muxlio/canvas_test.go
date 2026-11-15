package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCanvasCLI(t *testing.T) {
	// Test catalog
	if code := cmdCanvas([]string{"catalog"}, false); code != 0 {
		t.Fatalf("canvas catalog returned exit code %d", code)
	}
	if code := cmdCanvas([]string{"catalog", "--json"}, false); code != 0 {
		t.Fatalf("canvas catalog --json returned exit code %d", code)
	}

	// Test validate valid file
	validJSON := `{
		"version": 1,
		"title": "Valid Canvas",
		"content": {
			"type": "heading",
			"id": "h1",
			"level": 1,
			"text": "Hello Canvas"
		}
	}`
	tmpDir := t.TempDir()
	validPath := filepath.Join(tmpDir, "valid.canvas.json")
	if err := os.WriteFile(validPath, []byte(validJSON), 0644); err != nil {
		t.Fatal(err)
	}
	if code := cmdCanvas([]string{"validate", validPath}, false); code != 0 {
		t.Fatalf("canvas validate validPath returned %d", code)
	}
	if code := cmdCanvas([]string{"validate", validPath, "--json"}, false); code != 0 {
		t.Fatalf("canvas validate validPath --json returned %d", code)
	}

	// Test validate invalid file (duplicate ID or invalid version)
	invalidJSON := `{
		"version": 99,
		"title": "Invalid Canvas",
		"content": {
			"type": "heading",
			"id": "h1",
			"level": 1,
			"text": "Hello"
		}
	}`
	invalidPath := filepath.Join(tmpDir, "invalid.canvas.json")
	if err := os.WriteFile(invalidPath, []byte(invalidJSON), 0644); err != nil {
		t.Fatal(err)
	}
	if code := cmdCanvas([]string{"validate", invalidPath}, false); code != 1 {
		t.Fatalf("expected canvas validate invalidPath to return 1, got %d", code)
	}
}
