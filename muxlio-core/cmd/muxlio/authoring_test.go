package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-core/internal/plan"
)

func TestInstalledCLIAuthorsJSONWithoutNodeOrCheckout(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "muxlio")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	workspace := t.TempDir()
	submitted := false
	coordinator := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Plan  plan.Plan         `json:"plan"`
			Input map[string]string `json:"input"`
		}
		if r.Method != "POST" || r.URL.Path != "/v1/runs" {
			t.Errorf("unexpected submission: %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if issues := plan.Validate(&body.Plan); len(issues) != 0 || body.Input["message"] != "hello" {
			t.Errorf("invalid submission: %+v %+v", issues, body.Input)
		}
		submitted = true
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"runId":"template-run"}`))
	}))
	defer coordinator.Close()
	cli := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Dir = workspace
		// A binary-only environment: neither a checkout nor a package manager
		// can supply hidden dependencies while authoring or submitting.
		cmd.Env = []string{"PATH=", "HOME=" + workspace, "MUXLIO_HOME=" + workspace}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	for _, name := range []string{"echo", "task"} {
		raw := cli("template", name)
		if err := os.WriteFile(filepath.Join(workspace, name+".json"), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		out := cli("check", name+".json", "--json")
		var checked struct {
			Valid bool `json:"valid"`
		}
		if err := json.Unmarshal([]byte(out), &checked); err != nil || !checked.Valid {
			t.Fatalf("check: %s (%v)", out, err)
		}
	}
	if err := os.WriteFile(filepath.Join(workspace, "input.json"), []byte(`{"message":"hello"}`), 0600); err != nil {
		t.Fatal(err)
	}
	cli("build", "echo.json", "--out", "plan.json")
	if out := cli("test", "plan.json", "--input", "input.json", "--json"); !strings.Contains(out, `"succeeded"`) {
		t.Fatalf("test: %s", out)
	}
	cli("build", "task.json", "--out", "task-plan.json")
	if out := cli("test", "task-plan.json", "--input", "input.json", "--json"); !strings.Contains(out, `"succeeded"`) {
		t.Fatalf("test task: %s", out)
	}
	cli("submit", "--server", coordinator.URL, "plan.json", "input.json")
	if !submitted {
		t.Fatal("template was not submitted")
	}
}
