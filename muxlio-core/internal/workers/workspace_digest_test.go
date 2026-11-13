package workers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-core/internal/state"
)

func preparedTree(t *testing.T) string {
	t.Helper()
	path := t.TempDir()
	if err := os.WriteFile(filepath.Join(path, "data.txt"), []byte("prepared data\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	canonical, err := CanonicalWorkspace(path)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func treeDigest(t *testing.T, path string) string {
	t.Helper()
	digest, err := WorkspaceDigest(path)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func TestPreparedWorkspacePortableIdentity(t *testing.T) {
	laptop, mini := preparedTree(t), preparedTree(t)
	digest := treeDigest(t, laptop)
	if digest != "sha256:9648ba375b14222269640e00f12966a7e98dfab6642605d222a7704b087201f3" {
		t.Fatalf("version 1 digest encoding changed: %s", digest)
	}
	if treeDigest(t, mini) != digest {
		t.Fatal("host paths changed identity")
	}
	// Worktree administration metadata is host-local and never copied.
	if err := os.WriteFile(filepath.Join(mini, ".git"), []byte("gitdir: /host-specific/repo"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := (WorkspaceRegistry{"prepared": {WorkerID: "mini", Path: mini, Digest: digest}}).LocalBindings("mini")
	if err != nil {
		t.Fatal(err)
	}
	req := PrepareRemoteDispatch(ExecuteRequest{Workspace: laptop, WorkspaceID: "prepared", WorkspaceWorkerID: "mini", WorkspaceDigest: digest})
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	bound, err := ApplyAuthorizedWorkspace(req, "mini", registry, laptop)
	if err != nil {
		t.Fatal(err)
	}
	if bound.Workspace != mini || bound.WorkspaceDigest != digest {
		t.Fatalf("handoff lost prepared identity: %+v", bound)
	}
	snapshot, err := prepareAttemptWorkspace(bound, t.TempDir())
	if err != nil || treeDigest(t, snapshot.Workspace) != digest {
		t.Fatalf("snapshot did not retain prepared identity: %v", err)
	}
	// Mutation after registration is caught at execution, without fallback.
	if err := os.WriteFile(filepath.Join(mini, "data.txt"), []byte("wrong revision"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyAuthorizedWorkspace(req, "mini", registry, laptop); err == nil {
		t.Fatal("changed prepared contents accepted")
	}
	if _, err := prepareAttemptWorkspace(bound, t.TempDir()); err == nil {
		t.Fatal("changed contents copied into an attempt")
	}
	if _, err := registry.LocalBindings("mini"); err == nil {
		t.Fatal("wrong revision advertised at startup")
	}
	if err := os.RemoveAll(mini); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyAuthorizedWorkspace(req, "mini", registry, laptop); err == nil {
		t.Fatal("missing prepared workspace fell back to laptop checkout")
	}
}

func TestPreparedWorkspacePlacement(t *testing.T) {
	digest := treeDigest(t, preparedTree(t))
	req := ExecuteRequest{WorkspaceID: "prepared", WorkspaceWorkerID: "mini", WorkspaceDigest: digest}
	raw, _ := json.Marshal(req)
	caps := []state.WorkerCapability{{Name: "generic"}}
	labels := map[string]string{WorkspaceIdentityLabelPrefix + "prepared": "true", WorkspaceDigestLabelPrefix + "prepared": digest}
	if !MatchWorkerDispatch("mini", "generic", raw, caps, labels) {
		t.Fatal("matching prepared worker rejected")
	}
	if MatchWorkerDispatch("laptop", "generic", raw, caps, labels) {
		t.Fatal("wrong owner accepted")
	}
	for _, value := range []string{"", "sha256:wrong"} {
		labels[WorkspaceDigestLabelPrefix+"prepared"] = value
		if MatchWorkerDispatch("mini", "generic", raw, caps, labels) {
			t.Fatal("missing or wrong prepared revision accepted")
		}
	}
	registry := WorkspaceRegistry{"prepared": {WorkerID: "mini", Path: preparedTree(t), Digest: digest}}
	for _, value := range []string{"", "bad", "sha256:0000000000000000000000000000000000000000000000000000000000000000"} {
		req.WorkspaceDigest = value
		if _, err := ApplyAuthorizedWorkspace(req, "mini", registry, ""); err == nil {
			t.Fatal("execution with wrong or missing content pin accepted")
		}
	}
}

func TestWorkspaceDigestTracksSnapshotContents(t *testing.T) {
	for _, change := range []string{"bytes", "executable", "empty-directory", "symlink"} {
		t.Run(change, func(t *testing.T) {
			path := preparedTree(t)
			before := treeDigest(t, path)
			var err error
			switch change {
			case "bytes":
				err = os.WriteFile(filepath.Join(path, "data.txt"), []byte("new"), 0o600)
			case "executable":
				err = os.Chmod(filepath.Join(path, "data.txt"), 0o700)
			case "empty-directory":
				err = os.Mkdir(filepath.Join(path, "empty"), 0o700)
			case "symlink":
				err = os.Symlink("data.txt", filepath.Join(path, "link"))
			}
			if err != nil {
				t.Fatal(err)
			}
			if treeDigest(t, path) == before {
				t.Fatal("snapshot-relevant change did not change identity")
			}
		})
	}
	path := preparedTree(t)
	if err := os.Symlink(preparedTree(t), filepath.Join(path, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := WorkspaceDigest(path); err == nil {
		t.Fatal("external symlink accepted")
	}
}

func TestWorkspaceDigestRejectsLossyPathEncoding(t *testing.T) {
	// encoding/json replaces invalid UTF-8 with U+FFFD. Without rejection,
	// distinct Unix filenames such as these have the same content identity.
	for _, name := range []string{"input-\xff", "input-\xfe"} {
		t.Run(name, func(t *testing.T) {
			path := preparedTree(t)
			if err := os.WriteFile(filepath.Join(path, name), []byte("same bytes"), 0o600); err != nil {
				t.Skipf("filesystem cannot create non-UTF-8 names: %v", err)
			}
			if _, err := WorkspaceDigest(path); err == nil || !strings.Contains(err.Error(), "UTF-8") {
				t.Fatalf("expected explicit encoding rejection, got %v", err)
			}
		})
	}
	t.Run("symlink-target", func(t *testing.T) {
		path := preparedTree(t)
		if err := os.Symlink("input-\xff", filepath.Join(path, "link")); err != nil {
			t.Skipf("filesystem cannot create non-UTF-8 symlink targets: %v", err)
		}
		if _, err := WorkspaceDigest(path); err == nil || !strings.Contains(err.Error(), "UTF-8") {
			t.Fatalf("expected explicit encoding rejection, got %v", err)
		}
	})
	t.Run("valid-unicode-and-binary-content", func(t *testing.T) {
		path := preparedTree(t)
		if err := os.WriteFile(filepath.Join(path, "input-\ufffd"), []byte{0xff, 0xfe}, 0o600); err != nil {
			t.Fatal(err)
		}
		digest := treeDigest(t, path)
		snapshot, err := prepareAttemptWorkspace(ExecuteRequest{Workspace: path, WorkspaceDigest: digest}, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if treeDigest(t, snapshot.Workspace) != digest {
			t.Fatal("valid Unicode paths and binary contents did not survive snapshot")
		}
	})
}
