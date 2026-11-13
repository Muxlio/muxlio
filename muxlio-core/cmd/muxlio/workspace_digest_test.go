package main

import "testing"

func TestWorkspaceDigestCommand(t *testing.T) {
	if code := cmdWorkspace([]string{"digest", t.TempDir()}); code != 0 {
		t.Fatalf("digest exit = %d", code)
	}
	if code := cmdWorkspace([]string{"digest", t.TempDir() + "/missing"}); code != 1 {
		t.Fatalf("missing directory exit = %d", code)
	}
	if code := cmdWorkspace([]string{"digest"}); code != 2 {
		t.Fatalf("usage exit = %d", code)
	}
}
