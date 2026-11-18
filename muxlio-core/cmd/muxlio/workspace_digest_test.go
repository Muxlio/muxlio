package main

import "testing"

func TestWorkspaceDigestCommand(t *testing.T) {
	if code := cmdWorkspace([]string{"digest", t.TempDir()}, "", false); code != 0 {
		t.Fatalf("digest exit = %d", code)
	}
	if code := cmdWorkspace([]string{"digest", t.TempDir() + "/missing"}, "", false); code != 1 {
		t.Fatalf("missing directory exit = %d", code)
	}
	if code := cmdWorkspace([]string{"digest"}, "", false); code != 2 {
		t.Fatalf("usage exit = %d", code)
	}
}
