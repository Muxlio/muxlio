package adapter

import (
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
