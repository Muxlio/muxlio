package workers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/muxlio/muxlio-core/internal/state"
)

func TestDirectKeyedScriptExecutionFailsBeforeLaunch(t *testing.T) {
	s := testScript(t, `touch launched; printf '{}'`)
	for _, mode := range []string{"snapshot", "shared-write"} {
		var req ExecuteRequest
		// Admission cannot be spoofed by fields in a serialized request.
		if err := json.Unmarshal([]byte(`{"executor":"test","concurrencyKeys":["resource"],"resourcesAdmitted":true}`), &req); err != nil {
			t.Fatal(err)
		}
		req.WorkspaceMode = mode
		result, err := s.Execute(req)
		if err != nil || !strings.Contains(result.Error, "coordinator worker admission") {
			t.Fatalf("unreserved keyed execution accepted: %+v %v", result, err)
		}
	}
	entries, err := os.ReadDir(s.workBase)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("unadmitted execution created scratch files")
	}
}

func TestParallelAttemptSnapshotsOwnWritableTrees(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "file"), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	requests := make(chan ExecuteRequest, 2)
	errors := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		scratch := t.TempDir()
		wg.Go(func() {
			req, err := prepareAttemptWorkspace(ExecuteRequest{Workspace: source, Inputs: map[string]any{"workspace": source}}, scratch)
			requests <- req
			errors <- err
		})
	}
	wg.Wait()
	a, b := <-requests, <-requests
	for range 2 {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	if a.Workspace == b.Workspace || a.Workspace == source || a.Inputs["workspace"] != a.Workspace {
		t.Fatalf("shared workspace: %+v %+v", a, b)
	}
	if err := os.WriteFile(filepath.Join(a.Workspace, "file"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{source, b.Workspace} {
		data, err := os.ReadFile(filepath.Join(dir, "file"))
		if err != nil || string(data) != "original" {
			t.Fatalf("snapshot leaked: %q %v", data, err)
		}
	}
}

func TestEmptyWorkspaceDoesNotCopyWorkerSource(t *testing.T) {
	for _, source := range []string{t.TempDir(), filepath.Join(t.TempDir(), "missing")} {
		if info, err := os.Stat(source); err == nil && info.IsDir() {
			if err := os.WriteFile(filepath.Join(source, "source.txt"), []byte("private source"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		req := ExecuteRequest{Workspace: source, WorkspaceMode: "empty", Inputs: map[string]any{"workspace": source}}
		req, err := ApplyWorkerWorkspace(req, source)
		if err != nil {
			t.Fatal(err)
		}
		req, err = prepareAttemptWorkspace(req, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		entries, err := os.ReadDir(req.Workspace)
		if err != nil || len(entries) != 0 || req.Inputs["workspace"] != req.Workspace {
			t.Fatalf("workspace was not empty and rebound: %+v %v %v", req, entries, err)
		}
	}
}

func TestWorkspaceModesFailClosed(t *testing.T) {
	for _, req := range []ExecuteRequest{{WorkspaceMode: "shared-write"}, {WorkspaceMode: "read-only"}, {WorkspaceMode: "unknown"}, {ConcurrencyKeys: []string{""}}} {
		if _, err := prepareAttemptWorkspace(req, t.TempDir()); err == nil {
			t.Fatalf("accepted unsafe contract: %+v", req)
		}
	}
	source := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(source, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareAttemptWorkspace(ExecuteRequest{Workspace: source}, t.TempDir()); err == nil {
		t.Fatal("snapshot accepted host symlink")
	}
}

func TestWorkspaceSnapshotPreservesInternalSymlinks(t *testing.T) {
	source := t.TempDir()
	bin := filepath.Join(source, "node_modules", ".bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	tool := filepath.Join(source, "node_modules", "tool")
	if err := os.WriteFile(tool, []byte("original"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{
		"node_modules/.bin/tool": "../tool",
		"bin":                    "node_modules/.bin",
		"tool":                   "bin/tool",
	} {
		if err := os.Symlink(target, filepath.Join(source, name)); err != nil {
			t.Fatal(err)
		}
	}
	req, err := prepareAttemptWorkspace(ExecuteRequest{Workspace: source}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	link, err := os.Readlink(filepath.Join(req.Workspace, "node_modules", ".bin", "tool"))
	if err != nil || link != "../tool" {
		t.Fatalf("link not preserved: %q %v", link, err)
	}
	info, err := os.Stat(filepath.Join(req.Workspace, "tool"))
	if err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("executable target not preserved: %v %v", info, err)
	}
	if err := os.WriteFile(filepath.Join(req.Workspace, "tool"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(tool)
	if err != nil || string(data) != "original" {
		t.Fatalf("write through snapshot link changed source: %q %v", data, err)
	}
}

func TestWorkspaceSnapshotRejectsUnsafeSymlinks(t *testing.T) {
	for _, kind := range []string{"absolute", "escape", "chain", "dangling", "cycle"} {
		t.Run(kind, func(t *testing.T) {
			source := t.TempDir()
			target := "missing"
			switch kind {
			case "absolute":
				target = source
			case "escape":
				target = ".."
			case "chain":
				if err := os.Symlink("..", filepath.Join(source, "z-escape")); err != nil {
					t.Fatal(err)
				}
				target = "z-escape"
			case "cycle":
				target = "link"
			}
			if err := os.Symlink(target, filepath.Join(source, "link")); err != nil {
				t.Fatal(err)
			}
			if _, err := prepareAttemptWorkspace(ExecuteRequest{Workspace: source}, t.TempDir()); err == nil {
				t.Fatal("snapshot accepted unsafe symlink")
			}
		})
	}
}

func TestRequiredIsolationAdmissionNeedsEligibleBackend(t *testing.T) {
	caps := []state.WorkerCapability{{Name: "test"}}
	req := []byte(`{"params":{"isolation":{"mode":"required","runtime":"bwrap"}}}`)
	if MatchDispatch("test", req, caps, nil) {
		t.Fatal("untrusted request admitted without isolation")
	}
	labels := map[string]string{IsolationLabelPrefix + "bwrap": "true"}
	if !MatchDispatch("test", req, caps, labels) {
		t.Fatal("eligible backend rejected")
	}
	for _, policy := range []string{`{"mode":"required","runtime":"unknown"}`, `{"mode":"invalid"}`, `{"mode":"required","runtime":"oci"}`} {
		raw, _ := json.Marshal(map[string]any{"params": map[string]any{"isolation": json.RawMessage(policy)}})
		if MatchDispatch("test", raw, caps, labels) {
			t.Fatalf("accepted invalid backend %s", policy)
		}
	}
}

func TestOptionalVMAdmissionRequiresImageOptInAndLabel(t *testing.T) {
	caps := []state.WorkerCapability{{Name: "test"}}
	for _, backend := range []string{"docker-vm", "macos-vm"} {
		labels := map[string]string{IsolationLabelPrefix + backend: "true"}
		for _, enabled := range []bool{false, true} {
			for _, image := range []string{"", "approved-image"} {
				raw, _ := json.Marshal(map[string]any{"params": map[string]any{"isolation": map[string]any{"mode": "required", "runtime": backend, "enabled": enabled, "image": image}}})
				if got := MatchDispatch("test", raw, caps, labels); got != (enabled && image != "") {
					t.Fatalf("VM admission: enabled=%v image=%q matched=%v", enabled, image, got)
				}
				if MatchDispatch("test", raw, caps, nil) {
					t.Fatal("VM admitted without eligible worker")
				}
			}
		}
	}
}

func TestAttemptPathPartsDoNotAlias(t *testing.T) {
	if safePathPart("a/b") == safePathPart("ab") || safePathPart("a.b") == safePathPart("a/b") {
		t.Fatal("attempt identities alias")
	}
}

func TestScriptExecutorNeverReusesAttemptDirectory(t *testing.T) {
	s := testScript(t, `printf '{}'`)
	req := ExecuteRequest{RunID: "run", ActivationID: "activation", AttemptID: "attempt", Executor: "test"}
	if result, err := s.Execute(req); err != nil || result.Error != "" {
		t.Fatalf("first attempt: %+v %v", result, err)
	}
	if result, err := s.Execute(req); err != nil || result.Error != "" {
		t.Fatalf("restarted attempt: %+v %v", result, err)
	}
	paths, err := filepath.Glob(filepath.Join(s.workBase, "..", "runs", "run", "activation", "attempt", "execution-*", "workspace"))
	if err != nil || len(paths) != 2 {
		t.Fatalf("attempt workspace reused: %v %v", paths, err)
	}
}

func TestReadOnlyWorkspaceRequiresIsolationAtAdmission(t *testing.T) {
	caps := []state.WorkerCapability{{Name: "test"}}
	for _, raw := range []string{
		`{"workspaceMode":"read-only"}`,
		`{"workspaceMode":"read-only","params":{"isolation":{"mode":"trusted-native"}}}`,
		`{"workspaceMode":"shared-write"}`,
	} {
		if MatchDispatch("test", []byte(raw), caps, nil) {
			t.Fatalf("unsafe workspace admitted: %s", raw)
		}
	}
}
