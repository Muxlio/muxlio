package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveServerHonorsDataDirProfiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"servers":{"dev":"http://127.0.0.1:9999"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveServer("dev", root); err != nil {
		t.Fatalf("resolveServer with data dir: %v", err)
	}
	if _, _, err := resolveServer("dev", t.TempDir()); err == nil {
		t.Fatal("expected unknown profile outside configured runtime")
	}
}

func TestResolveServerNeverSendsRootTokenToRemoteProfiles(t *testing.T) {
	t.Setenv("MUXLIO_TOKEN", "")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "remote.token"), []byte("scoped-from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := `{"token":"root-admin","addr":"127.0.0.1:7600","servers":{
		"local":{"url":"http://127.0.0.1:7600","credentialRef":"root"},
		"legacy-local":"http://127.0.0.1:7600",
		"remote":{"url":"https://remote.example"},
		"remote-file":{"url":"https://remote.example","tokenFile":"remote.token"},
		"remote-root":{"url":"https://remote.example","credentialRef":"root"},
		"plaintext":{"url":"http://203.0.113.5:7600","token":"scoped"},
		"other-loopback":{"url":"http://127.0.0.1:9999"}}}`
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"local": "root-admin", "legacy-local": "root-admin", "remote": "", "remote-file": "scoped-from-file", "other-loopback": ""} {
		_, token, err := resolveServer(name, root)
		if err != nil || token != want {
			t.Errorf("%s: token=%q err=%v", name, token, err)
		}
	}
	for _, name := range []string{"remote-root", "plaintext"} {
		if _, token, err := resolveServer(name, root); err == nil {
			t.Errorf("%s accepted with token %q", name, token)
		}
	}
}

func TestCLIRunAcceptsServerAfterPositionals(t *testing.T) {
	if os.Getenv("MUXLIO_TEST_CLI") == "1" {
		return
	}
	submissions := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			submissions++
			io.WriteString(w, `{"runId":"run-remote","state":"succeeded"}`)
			return
		}
		io.WriteString(w, `{"id":"run-remote","state":"succeeded","output":{"path":"summary.md"}}`)
	}))
	defer s.Close()

	bin := os.Args[0]
	dir := t.TempDir()
	plan := filepath.Join(dir, "plan.json")
	if err := os.WriteFile(plan, []byte(`{"irVersion":"1.0","name":"remote","root":"root","subgraphs":{"root":{"nodes":[]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-test.run=^TestCLIProcess$", "--", "run", plan, "--server", s.URL, "--json")
	cmd.Env = append(os.Environ(), "MUXLIO_TEST_CLI=1", "MUXLIO_HOME="+dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run with trailing --server: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), `"state":"succeeded"`) {
		t.Fatalf("unexpected output: %s", out)
	}
	if submissions != 1 {
		t.Fatalf("submissions=%d want 1", submissions)
	}
}

func TestCLISignalAcceptsGlobalTokenBeforeCommand(t *testing.T) {
	if os.Getenv("MUXLIO_TEST_CLI") == "1" {
		return
	}
	var gotAuth string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"delivered":true}`))
	}))
	defer s.Close()

	bin := os.Args[0]
	cmd := exec.Command(bin, "-test.run=^TestCLIProcess$", "--", "--token", "global-tok", "--server", s.URL, "signal", "run-1", "--name", "sig")
	cmd.Env = append(os.Environ(), "MUXLIO_TEST_CLI=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("global --token before signal: %v\n%s", err, out)
	}
	if gotAuth != "Bearer global-tok" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
}

func TestCLIStatusAcceptsGlobalFlagsBeforeCommand(t *testing.T) {
	if os.Getenv("MUXLIO_TEST_CLI") == "1" {
		return
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "run-1", "state": "succeeded"})
	}))
	defer s.Close()

	bin := os.Args[0]
	run := func(argv ...string) (string, int) {
		t.Helper()
		cmd := exec.Command(bin, append([]string{"-test.run=^TestCLIProcess$", "--"}, argv...)...)
		cmd.Env = append(os.Environ(), "MUXLIO_TEST_CLI=1")
		out, _ := cmd.CombinedOutput()
		return string(out), cmd.ProcessState.ExitCode()
	}

	out, code := run("--server", s.URL, "status", "run-1")
	if code != 0 {
		t.Fatalf("global --server before status: exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "run-1") && !strings.Contains(out, "succeeded") {
		t.Fatalf("unexpected output: %s", out)
	}

	out, code = run("--server="+s.URL, "status", "run-1")
	if code != 0 {
		t.Fatalf("global --server=URL before status: exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "run-1") && !strings.Contains(out, "succeeded") {
		t.Fatalf("unexpected output: %s", out)
	}
}
