package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunAllowsValidationWarnings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plan.json")
	raw := `{"irVersion":"1.0","name":"warnings","root":"root","subgraphs":{"root":{"nodes":[{"id":"work","kind":"execute","executor":"generic/task","fallbackExecutors":["generic/task"]}]}}}`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	submissions := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			submissions++
			io.WriteString(w, `{"runId":"run-test","state":"succeeded"}`)
		} else {
			io.WriteString(w, `{"id":"run-test","state":"succeeded"}`)
		}
	}))
	defer server.Close()
	for _, target := range []string{"", server.URL} {
		old, r, w := captureStdout(t)
		code := cmdRun(path, "", "", "", dir, "", target, "", "", true)
		w.Close()
		restoreStdout(old)
		out, _ := io.ReadAll(r)
		r.Close()
		if code != 0 || !strings.Contains(string(out), `"succeeded"`) {
			t.Fatalf("target=%q code=%d output=%s", target, code, out)
		}
	}
	if submissions != 1 {
		t.Fatalf("submissions=%d", submissions)
	}
}

func TestRunRejectsMissingBindingBeforeDispatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plan.json")
	raw := `{"irVersion":"1.0","name":"preflight","root":"root","subgraphs":{"root":{"nodes":[{"id":"paid","kind":"execute","executor":"generic/task"},{"id":"later","kind":"project","inputs":{"x":{"expr":{"op":"ref","node":"missing","port":"x"}}}}]}}}`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	submissions := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { submissions++; w.WriteHeader(500) }))
	defer server.Close()
	for _, target := range []string{"", server.URL} {
		// An invalid adapter/workspace would return 2 if preflight did not run first.
		if code := cmdRun(path, "", "", filepath.Join(dir, "absent-adapters"), dir, "relative", target, "", "", true); code != 1 {
			t.Fatalf("target=%q code=%d", target, code)
		}
	}
	if submissions != 0 {
		t.Fatal("invalid plan submitted to coordinator")
	}
	if _, err := os.Stat(filepath.Join(dir, "data/coordinator.db")); !os.IsNotExist(err) {
		t.Fatal("invalid plan created local runtime state")
	}
}
