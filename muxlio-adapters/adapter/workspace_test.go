package adapter

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceRequiresExplicitAbsoluteDirectory(t *testing.T) {
	for _, workspace := range []string{"", ".", "relative/project"} {
		if _, err := WorkspaceRoot(&Request{Workspace: workspace}); err == nil {
			t.Fatalf("accepted implicit workspace %q", workspace)
		}
	}
	workspace := t.TempDir()
	got, err := WorkspaceRoot(&Request{Workspace: workspace})
	expected, _ := filepath.EvalSymlinks(workspace)
	if err != nil || got != expected {
		t.Fatalf("workspace=%q err=%v", got, err)
	}
}

func TestResolveOutputAllowsMissingFileAndRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	if path, err := ResolveOutput(root, "pending.json"); err != nil || path != filepath.Join(root, "pending.json") {
		t.Fatalf("missing output path %q err=%v", path, err)
	}
	if _, err := ResolveOutput(root, "../secret"); err == nil {
		t.Fatal("accepted traversal")
	}
	out := t.TempDir()
	if err := os.Symlink(out, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveOutput(root, "escape/pending.json"); err == nil {
		t.Fatal("accepted symlink escape for missing output")
	}
	if err := os.Symlink(filepath.Join(out, "not-created"), filepath.Join(root, "dangling")); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveOutput(root, "dangling/pending.json"); err == nil {
		t.Fatal("accepted dangling symlink escape for missing output")
	}
}

func TestValidateOutputPathRejectsTraversalAndEscapingSymlink(t *testing.T) {
	root := t.TempDir()
	if err := ValidateOutputPath(root, "out/result.json"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateOutputPath(root, "../secret"); err == nil {
		t.Fatal("accepted traversal")
	}
	out := t.TempDir()
	if err := os.Symlink(out, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := ValidateOutputPath(root, "escape/file"); err == nil {
		t.Fatal("accepted symlink escape")
	}
}
