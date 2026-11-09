package workers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
		ref, ok, parseErr := artifactRefFromValue(result.Outputs["result"])
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		if !ok || ref.Path != "result.txt" || ref.Size != 7 {
			t.Fatalf("missing uploaded artifact: %+v", result)
		}
	}
	paths, err := filepath.Glob(filepath.Join(inner.workBase, "..", "runs", "run", "activation", "attempt", "execution-*", "result.txt"))
	if err != nil || len(paths) != 2 || uploads != 2 {
		t.Fatalf("invocations shared scratch or lost output: %v uploads=%d err=%v", paths, uploads, err)
	}
}

func TestMaterializeInputsPreservesDistinctDeclaredPaths(t *testing.T) {
	leftDigest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("left-bytes")))
	rightDigest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("right-bytes")))
	payloads := map[string]string{leftDigest: "left-bytes", rightDigest: "right-bytes"}
	client := &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		if strings.HasSuffix(req.URL.Path, "/transfer") {
			digest := strings.TrimSuffix(strings.TrimPrefix(req.URL.Path, "/v1/artifacts/"), "/transfer")
			_ = json.NewEncoder(w).Encode(artifacts.TransferPlan{Digest: digest, URL: "/bytes/" + digest[7:], Method: req.URL.Query().Get("method"), Mode: "proxy"})
			return w.Result(), nil
		}
		if strings.HasPrefix(req.URL.Path, "/bytes/") {
			hex := strings.TrimPrefix(req.URL.Path, "/bytes/")
			for digest, payload := range payloads {
				if strings.HasSuffix(digest, hex) {
					_, _ = io.WriteString(w, payload)
					return w.Result(), nil
				}
			}
		}
		w.WriteHeader(http.StatusNotFound)
		return w.Result(), nil
	})}
	a := ArtifactTransferExecutor{Client: artifacts.Client{BaseURL: "http://coordinator", HTTP: client}, WorkBase: t.TempDir()}
	req := ExecuteRequest{
		RunID: "run", ActivationID: "act", AttemptID: "attempt",
		Inputs: map[string]any{
			"left":  map[string]any{"digest": leftDigest, "size": json.Number("10"), "path": "foo/bar.txt"},
			"right": map[string]any{"digest": rightDigest, "size": json.Number("11"), "path": "baz/bar.txt"},
		},
	}
	if err := a.materializeInputs(&req); err != nil {
		t.Fatal(err)
	}
	for side, want := range map[string]string{"left": "left-bytes", "right": "right-bytes"} {
		ref, ok := req.Inputs[side].(map[string]any)
		if !ok {
			t.Fatalf("%s input not a map", side)
		}
		path, _ := ref["path"].(string)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s path %q: %v", side, path, err)
		}
		if string(data) != want {
			t.Fatalf("%s = %q, want %q", side, data, want)
		}
	}
	leftPath := req.Inputs["left"].(map[string]any)["path"].(string)
	rightPath := req.Inputs["right"].(map[string]any)["path"].(string)
	if leftPath == rightPath {
		t.Fatalf("inputs aliased to the same file: %q", leftPath)
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
		if processGroupHasLiveMembers(pid) {
			t.Fatal("process group still has a live process")
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

func TestInlineOutputRejectsEscapingSymlinksBeforeMutation(t *testing.T) {
	for _, kind := range []string{"leaf", "parent", "nested-parent"} {
		t.Run(kind, func(t *testing.T) {
			scratch, outside := t.TempDir(), t.TempDir()
			victim := filepath.Join(outside, "victim.txt")
			if err := os.WriteFile(victim, []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			decl := "result.txt"
			link, target := filepath.Join(scratch, decl), victim
			if kind != "leaf" {
				link, target = filepath.Join(scratch, "reports"), outside
				decl = "reports/victim.txt"
				if kind == "nested-parent" {
					decl = "reports/new/deep/result.txt"
				}
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			a, uploads := countingArtifactPublisher()
			result, err := a.publishOutputs(ExecuteRequest{attemptScratch: scratch, Outputs: map[string]OutputDecl{"report": {Path: decl}}}, inlineReportResult())
			if err == nil && result.Error == "" {
				t.Error("escaping inline output succeeded")
			}
			if *uploads != 0 {
				t.Errorf("uploaded rejected output: %d", *uploads)
			}
			data, err := os.ReadFile(victim)
			if err != nil || string(data) != "original" {
				t.Errorf("outside victim changed: %q, %v", data, err)
			}
			if _, err := os.Stat(filepath.Join(outside, "new")); !os.IsNotExist(err) {
				t.Errorf("created outside directory: %v", err)
			}
		})
	}
}

func TestInlineOutputMaterializationErrorsFailPublication(t *testing.T) {
	for _, kind := range []string{"read-only-leaf", "blocked-parent", "directory-leaf", "invalid-path"} {
		t.Run(kind, func(t *testing.T) {
			scratch := t.TempDir()
			decl := "result.txt"
			switch kind {
			case "read-only-leaf":
				path := filepath.Join(scratch, decl)
				if err := os.WriteFile(path, []byte("stale"), 0400); err != nil {
					t.Fatal(err)
				}
				// Permission-based failure is unavailable to privileged test processes.
				if f, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
					f.Close()
					t.Skip("process can write read-only files")
				}
			case "blocked-parent":
				if err := os.WriteFile(filepath.Join(scratch, "reports"), nil, 0600); err != nil {
					t.Fatal(err)
				}
				decl = "reports/result.txt"
			case "directory-leaf":
				if err := os.Mkdir(filepath.Join(scratch, decl), 0700); err != nil {
					t.Fatal(err)
				}
			case "invalid-path":
				decl = "../result.txt"
			}
			a, uploads := countingArtifactPublisher()
			result, err := a.publishOutputs(ExecuteRequest{attemptScratch: scratch, Outputs: map[string]OutputDecl{"report": {Path: decl}}}, inlineReportResult())
			if err == nil && result.Error == "" {
				t.Errorf("materialization failure reported success: %+v", result)
			}
			if *uploads != 0 {
				t.Errorf("uploaded failed inline output: %d", *uploads)
			}
		})
	}
}

func TestInlineOutputMaterializesNestedPath(t *testing.T) {
	scratch := t.TempDir()
	a, uploads := countingArtifactPublisher()
	result, err := a.publishOutputs(ExecuteRequest{attemptScratch: scratch, Outputs: map[string]OutputDecl{"report": {Path: "reports/new/result.txt"}}}, inlineReportResult())
	if err != nil || result.Error != "" {
		t.Fatalf("inline output failed: %+v %v", result, err)
	}
	ref, ok, parseErr := artifactRefFromValue(result.Outputs["report"])
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	want := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("replacement")))
	if !ok || ref.Digest != want || ref.Size != 11 || *uploads != 1 {
		t.Fatalf("wrong inline artifact: %+v uploads=%d", result, *uploads)
	}
}

func inlineReportResult() ExecuteResult {
	return ExecuteResult{Outputs: map[string]any{"report": map[string]any{"data": "replacement"}}}
}

func countingArtifactPublisher() (ArtifactTransferExecutor, *int) {
	uploads := new(int)
	client := &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		if strings.HasSuffix(req.URL.Path, "/transfer") {
			_ = json.NewEncoder(w).Encode(artifacts.TransferPlan{Digest: strings.TrimSuffix(strings.TrimPrefix(req.URL.Path, "/v1/artifacts/"), "/transfer"), URL: "/bytes", Method: req.URL.Query().Get("method"), Mode: "proxy"})
		} else if req.Method == http.MethodPut {
			*uploads++
			_, _ = io.Copy(io.Discard, req.Body)
			_ = req.Body.Close()
		}
		return w.Result(), nil
	})}
	return ArtifactTransferExecutor{Client: artifacts.Client{BaseURL: "http://coordinator", HTTP: client}}, uploads
}

func TestInlineOutputContainsConcurrentSymlinkReplacement(t *testing.T) {
	for _, parent := range []bool{false, true} {
		t.Run(fmt.Sprintf("parent=%v", parent), func(t *testing.T) {
			scratch, outside := t.TempDir(), t.TempDir()
			victim := filepath.Join(outside, "victim.txt")
			if err := os.WriteFile(victim, []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			decl, insideTarget, outsideTarget := "result.txt", "inside.txt", victim
			link := filepath.Join(scratch, "result.txt")
			if parent {
				decl, insideTarget, outsideTarget = "reports/new/result.txt", "inside", outside
				link = filepath.Join(scratch, "reports")
				if err := os.Mkdir(filepath.Join(scratch, "inside"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(insideTarget, link); err != nil {
				t.Fatal(err)
			}
			stop, done, ready := make(chan struct{}), make(chan error, 1), make(chan struct{})
			go func() {
				close(ready)
				for {
					for _, target := range []string{outsideTarget, insideTarget} {
						select {
						case <-stop:
							done <- nil
							return
						default:
						}
						next := filepath.Join(scratch, "next-link")
						if err := os.Symlink(target, next); err != nil {
							done <- err
							return
						}
						if err := os.Rename(next, link); err != nil {
							done <- err
							return
						}
					}
				}
			}()
			<-ready
			a, uploads := countingArtifactPublisher()
			for range 100 {
				before := *uploads
				result, err := a.publishOutputs(ExecuteRequest{attemptScratch: scratch, Outputs: map[string]OutputDecl{"report": {Path: decl}}}, inlineReportResult())
				if (err != nil || result.Error != "") && *uploads != before {
					t.Error("failed publication uploaded bytes")
				}
			}
			close(stop)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(victim)
			if err != nil || string(data) != "original" {
				t.Errorf("concurrent replacement changed outside victim: %q %v", data, err)
			}
			if _, err := os.Stat(filepath.Join(outside, "new")); !os.IsNotExist(err) {
				t.Errorf("concurrent replacement created outside directory: %v", err)
			}
		})
	}
}

func TestArtifactReferenceSize(t *testing.T) {
	for _, value := range []any{json.Number("7"), json.Number("7.0"), json.Number("7e0"), json.Number("70e-1"), json.Number("0.0"), json.Number("0e999999999999999999999"), json.Number("-0"), json.Number("9223372036854775807.0"), int(7), int64(7), float64(7), json.Number("9223372036854775807"), json.Number("0")} {
		ref, ok, err := artifactRefFromValue(map[string]any{"digest": "sha256:test", "size": value})
		if !ok || err != nil {
			t.Errorf("%v (%T): ref=%+v ok=%v err=%v", value, value, ref, ok, err)
		}
	}
	for _, value := range []any{json.Number("9223372036854775808"), json.Number("1.5"), json.Number("7.0000000000000000001"), json.Number("1e-999999999999999999"), json.Number("1e999999999999999999"), json.Number("1e400"), json.Number("-1"), float64(-1), float64(0x1p63), 1.5, math.Inf(1), math.NaN(), "7", nil} {
		if _, _, err := artifactRefFromValue(map[string]any{"digest": "sha256:test", "size": value}); err == nil {
			t.Errorf("accepted %v (%T)", value, value)
		}
	}
	if ref, ok, err := artifactRefFromValue(map[string]any{"digest": "sha256:test"}); !ok || err != nil || ref.Size != 0 {
		t.Fatalf("absent size: ref=%+v ok=%v err=%v", ref, ok, err)
	}
}
