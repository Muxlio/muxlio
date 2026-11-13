package workers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/muxlio/muxlio-core/internal/state"
)

func TestAuthorizedWorkspaceBindings(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(first, alias); err != nil {
		t.Fatal(err)
	}
	registry, err := (WorkspaceRegistry{
		"first":  {WorkerID: "worker", Path: alias, Aliases: []string{"client-a", "client-b"}},
		"second": {WorkerID: "worker", Path: second},
	}).LocalBindings("worker")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"first", "client-a", "client-b", "second"} {
		req, err := ApplyAuthorizedWorkspace(ExecuteRequest{WorkspaceID: id, WorkspaceWorkerID: "worker"}, "worker", registry, second)
		if err != nil {
			t.Fatal(err)
		}
		wantID, wantPath := "first", first
		if id == "second" {
			wantID, wantPath = "second", second
		}
		wantPath, _ = CanonicalWorkspace(wantPath)
		if req.WorkspaceID != wantID || req.Workspace != wantPath || req.Inputs["workspace"] != wantPath {
			t.Fatalf("wrong binding: %+v", req)
		}
	}
	for _, path := range []string{first, alias} {
		req := ExecuteRequest{WorkspaceID: "first", WorkspaceWorkerID: "worker", Inputs: map[string]any{"workspace": map[string]any{"data": path}}}
		if _, err := ApplyAuthorizedWorkspace(req, "worker", registry, ""); err != nil {
			t.Fatal(err)
		}
	}
	for name, req := range map[string]ExecuteRequest{
		"unknown":                    {WorkspaceID: "unknown", WorkspaceWorkerID: "worker"},
		"path-as-id":                 {WorkspaceID: first, WorkspaceWorkerID: "worker"},
		"missing-id":                 {},
		"wrong-owner":                {WorkspaceID: "first", WorkspaceWorkerID: "other"},
		"unauthorized-existing-path": {WorkspaceID: "first", WorkspaceWorkerID: "worker", Inputs: map[string]any{"workspace": second}},
		"missing-worktree":           {WorkspaceID: "first", WorkspaceWorkerID: "worker", Inputs: map[string]any{"workspace": filepath.Join(first, "missing")}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ApplyAuthorizedWorkspace(req, "worker", registry, second); err == nil {
				t.Fatal("unauthorized selection accepted")
			}
		})
	}
	// The configured alias was resolved at startup; retargeting it cannot
	// silently change the workspace associated with this identity.
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(second, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyAuthorizedWorkspace(ExecuteRequest{WorkspaceID: "first", WorkspaceWorkerID: "worker", Inputs: map[string]any{"workspace": alias}}, "worker", registry, second); err == nil {
		t.Fatal("retargeted alias accepted")
	}
	if err := os.RemoveAll(first); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyAuthorizedWorkspace(ExecuteRequest{WorkspaceID: "first", WorkspaceWorkerID: "worker"}, "worker", registry, second); err == nil {
		t.Fatal("removed workspace fell back to another checkout")
	}
}

func TestWorkspaceOwnershipRoutesDispatch(t *testing.T) {
	caps := []state.WorkerCapability{{Name: "test"}}
	raw, _ := json.Marshal(ExecuteRequest{WorkspaceID: "project", WorkspaceWorkerID: "owner"})
	labels := map[string]string{WorkspaceIdentityLabelPrefix + "project": "true"}
	if !MatchWorkerDispatch("owner", "test", raw, caps, labels) {
		t.Fatal("owner cannot claim")
	}
	if MatchWorkerDispatch("other", "test", raw, caps, labels) {
		t.Fatal("another worker claimed owner workspace")
	}
	if MatchWorkerDispatch("owner", "test", raw, caps, nil) {
		t.Fatal("worker without binding claimed workspace")
	}
}

func TestPinnedWorkspaceRejectsReplacementDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkout")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	registry, err := (WorkspaceRegistry{"project": {WorkerID: "owner", Path: path}}).LocalBindings("owner")
	if err != nil {
		t.Fatal(err)
	}
	// Keep the old directory alive to rule out filesystem inode reuse.
	if err := os.Rename(path, path+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyAuthorizedWorkspace(ExecuteRequest{WorkspaceID: "project", WorkspaceWorkerID: "owner"}, "owner", registry, ""); err == nil {
		t.Fatal("replacement checkout accepted")
	}
	if _, err := registry.LocalBindings("owner"); err == nil {
		t.Fatal("replacement checkout accepted while repinning worker configuration")
	}
}

func TestWorkspaceRegistryRejectsAmbiguousAliasesAndRemoteMissingWorktree(t *testing.T) {
	r := WorkspaceRegistry{"one": {WorkerID: "remote", Path: "/missing/remote/worktree", Aliases: []string{"two"}}, "two": {WorkerID: "remote", Path: t.TempDir()}}
	if err := r.Validate(); err == nil {
		t.Fatal("ambiguous alias accepted")
	}
	delete(r, "two")
	if _, err := r.LocalBindings("remote"); err == nil {
		t.Fatal("missing remote worktree accepted")
	}
	if _, err := r.LocalBindings("coordinator"); err != nil {
		t.Fatalf("coordinator must not resolve remote paths: %v", err)
	}
}

func TestWorkspaceModesKeepTheirMeaning(t *testing.T) {
	path := t.TempDir()
	r, err := (WorkspaceRegistry{"project": {WorkerID: "owner", Path: path}}).LocalBindings("owner")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"snapshot", "read-only", "shared-write", "empty"} {
		req, err := ApplyAuthorizedWorkspace(ExecuteRequest{WorkspaceID: "project", WorkspaceWorkerID: "owner", WorkspaceMode: mode}, "owner", r, "")
		if err != nil || req.WorkspaceMode != mode {
			t.Fatalf("mode changed: %+v %v", req, err)
		}
		if mode == "empty" && req.Workspace != "" {
			t.Fatal("empty mode bound a source")
		}
	}
}
