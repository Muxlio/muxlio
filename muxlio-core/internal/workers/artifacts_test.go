package workers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/artifacts"
)

func TestScriptArtifactTransferSharesUniqueInvocationScratch(t *testing.T) {
	inner := testScript(t, `cat input.txt > result.txt; printf '{}'`)
	uploads := 0
	client := &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		if strings.HasSuffix(req.URL.Path, "/transfer") {
			_ = json.NewEncoder(w).Encode(artifacts.TransferPlan{Digest: strings.TrimSuffix(strings.TrimPrefix(req.URL.Path, "/v1/artifacts/"), "/transfer"), URL: "/bytes", Method: req.URL.Query().Get("method"), Mode: "proxy"})
		} else if req.Method == http.MethodPut {
			data, err := io.ReadAll(req.Body)
			if err != nil || string(data) != "payload" {
				t.Fatalf("uploaded file: %q %v", data, err)
			}
			_ = req.Body.Close()
			uploads++
		} else {
			_, _ = io.WriteString(w, "payload")
		}
		return w.Result(), nil
	})}
	wrap := ArtifactTransferExecutor{Inner: inner, WorkBase: inner.workBase,
		Client: artifacts.Client{BaseURL: "http://coordinator", HTTP: client}}
	req := ExecuteRequest{Executor: "test", RunID: "run", ActivationID: "activation", AttemptID: "attempt",
		Inputs: map[string]any{"input": map[string]any{
			"digest": fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("payload"))), "size": float64(7), "path": "input.txt",
		}},
		Outputs: map[string]OutputDecl{"result": {Path: "result.txt", Mime: "text/plain"}}}
	for range 2 {
		result, err := wrap.Execute(req)
		if err != nil || result.Error != "" {
			t.Fatalf("file artifact transfer failed: %+v %v", result, err)
		}
		ref, ok := artifactRefFromValue(result.Outputs["result"])
		if !ok || ref.Path != "result.txt" || ref.Size != 7 {
			t.Fatalf("missing uploaded artifact: %+v", result)
		}
	}
	paths, err := filepath.Glob(filepath.Join(inner.workBase, "..", "runs", "run", "activation", "attempt", "execution-*", "result.txt"))
	if err != nil || len(paths) != 2 || uploads != 2 {
		t.Fatalf("invocations shared scratch or lost output: %v uploads=%d err=%v", paths, uploads, err)
	}
}

func TestStripInlineArtifactBytes(t *testing.T) {
	req := ExecuteRequest{Inputs: map[string]any{
		"doc": map[string]any{
			"digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"mime":   "text/plain",
			"data":   "secret-bytes",
		},
		"count": float64(3),
	}}
	got := StripInlineArtifactBytes(req)
	doc := got.Inputs["doc"].(map[string]any)
	if _, ok := doc["data"]; ok {
		t.Fatal("data field must be stripped")
	}
	if doc["digest"] == "" {
		t.Fatal("digest must remain")
	}
}

func TestArtifactTransferExecutorRespectsContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "pid")
	readyPath := filepath.Join(dir, "ready")
	inner := testScript(t, "echo $$ > '"+pidPath+"'\nsleep 30 &\necho 1 > '"+readyPath+"'\nwait\n")
	wrap := ArtifactTransferExecutor{Inner: inner, WorkBase: t.TempDir()}
	pidReady := make(chan int, 1)
	go func() {
		for {
			if _, err := os.ReadFile(readyPath); err != nil {
				time.Sleep(time.Millisecond)
				continue
			}
			data, err := os.ReadFile(pidPath)
			if err != nil {
				time.Sleep(time.Millisecond)
				continue
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil {
				time.Sleep(time.Millisecond)
				continue
			}
			pidReady <- pid
			cancel()
			return
		}
	}()
	_, _ = wrap.Execute(ExecuteRequest{Executor: "test", Context: ctx})
	select {
	case pid := <-pidReady:
		if err := syscall.Kill(-pid, 0); err != syscall.ESRCH {
			t.Fatalf("process group still exists: %v", err)
		}
	default:
		t.Fatal("adapter never started")
	}
}

func TestVerifyOutputArtifacts(t *testing.T) {
	store, err := artifacts.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.PutStream(t.Context(), strings.NewReader("x"), "text/plain", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyOutputArtifacts(store, map[string]any{"out": map[string]any{"digest": ref.Digest}}); err != nil {
		t.Fatal(err)
	}
	missing := "sha256:" + strings.Repeat("b", 64)
	if err := VerifyOutputArtifacts(store, map[string]any{"out": map[string]any{"digest": missing}}); err == nil {
		t.Fatal("expected missing artifact error")
	}
}

func TestPublishOutputsSkipsScalars(t *testing.T) {
	// Scalar outputs (decl.Path == "") must pass through unchanged and must NOT
	// be treated as files; trying to read them as files would fail if the "file"
	// is actually a directory (the root cause of the prepare-step crash).
	dir := t.TempDir()

	// Simulate an agent that returned a raw boolean for "passed" and a string
	// for "path" — both scalars (no decl.Path).
	inner := &stubExecutor{result: ExecuteResult{
		Outputs: map[string]any{
			"passed": true,
			"path":   "/srv/muxlio",
		},
	}}
	store, err := artifacts.NewLocal(dir)
	if err != nil {
		t.Fatal(err)
	}
	wrap := ArtifactTransferExecutor{
		Inner:    inner,
		Client:   artifacts.Client{BaseURL: "http://unused"},
		WorkBase: dir,
	}
	result, err := wrap.Execute(ExecuteRequest{
		Executor: "test",
		Outputs: map[string]OutputDecl{
			"passed": {Mime: "application/json"}, // scalar: no Path
			"path":   {Mime: "application/json"}, // scalar: no Path
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if result.Outputs["passed"] != true {
		t.Errorf("passed = %v, want true", result.Outputs["passed"])
	}
	if result.Outputs["path"] != "/srv/muxlio" {
		t.Errorf("path = %v, want /srv/muxlio", result.Outputs["path"])
	}
	_ = store // silence unused-import warning
}

func TestPublishOutputsAllowsNestedDeclaredPath(t *testing.T) {
	scratch := t.TempDir()
	path := filepath.Join(scratch, "reports", "result.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("report"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := testArtifactPublisher()
	result, err := a.publishOutputs(ExecuteRequest{attemptScratch: scratch, Outputs: map[string]OutputDecl{"report": {Path: "reports/result.txt"}}}, ExecuteResult{Outputs: map[string]any{"report": map[string]any{"path": path}}})
	if err != nil || result.Error != "" {
		t.Fatalf("nested declared output rejected: %+v %v", result, err)
	}
}

func TestPublishOutputsRejectsSymlinkOutsideScratch(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "host-private.txt")
	if err := os.WriteFile(outside, []byte("host-private-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	scratch := t.TempDir()
	path := filepath.Join(scratch, "result.txt")
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	a := testArtifactPublisher()
	result, err := a.publishOutputs(ExecuteRequest{attemptScratch: scratch, Outputs: map[string]OutputDecl{"report": {Path: "result.txt"}}}, ExecuteResult{Outputs: map[string]any{"report": map[string]any{"path": path}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Error == "" || !strings.Contains(result.Error, "open output") {
		t.Fatalf("outside symlink was not rejected: %+v", result)
	}
}

func testArtifactPublisher() ArtifactTransferExecutor {
	client := &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		if strings.HasSuffix(req.URL.Path, "/transfer") {
			_ = json.NewEncoder(w).Encode(artifacts.TransferPlan{Digest: strings.TrimSuffix(strings.TrimPrefix(req.URL.Path, "/v1/artifacts/"), "/transfer"), URL: "/bytes", Method: req.URL.Query().Get("method"), Mode: "proxy"})
		}
		return w.Result(), nil
	})}
	return ArtifactTransferExecutor{Client: artifacts.Client{BaseURL: "http://coordinator", HTTP: client}}
}

// stubExecutor always returns a fixed result.
type stubExecutor struct{ result ExecuteResult }

func (s *stubExecutor) Execute(_ ExecuteRequest) (ExecuteResult, error) {
	return s.result, nil
}
