package main

import (
	"encoding/json"
	"github.com/muxlio/muxlio-core/internal/diagnostic"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func checkJSON(t *testing.T, path string) (int, []diagnostic.Diagnostic) {
	t.Helper()
	old, r, w := captureStdout(t)
	code := cmdCheck(path, true)
	_ = w.Close()
	restoreStdout(old)
	b, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Errors []diagnostic.Diagnostic `json:"errors"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("non-JSON check output: %s: %v", b, err)
	}
	return code, out.Errors
}

func TestCheckJSONRepairAndLoadFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.json")
	raw := `{"irVersion":"1.0","name":"repair","root":"root","subgraphs":{"root":{"nodes":[{"id":"task","kind":"execute","executor":"generic/task","inputs":{"x":{"ref":{"node":"missing","port":"x"}}}}]}}}`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	code, issues := checkJSON(t, path)
	if code != 1 || len(issues) != 1 || issues[0].Code != "MUXLIO_BAD_REF" || issues[0].Source.File != path {
		t.Fatalf("code=%d issues=%+v", code, issues)
	}
	_, again := checkJSON(t, path)
	if issues[0].ID != again[0].ID {
		t.Fatal("unstable check identifier")
	}
	fixed := strings.Replace(raw, `"ref":{"node":"missing","port":"x"}`, `"expr":{"op":"literal","value":"fixed"}`, 1)
	if err := os.WriteFile(path, []byte(fixed), 0600); err != nil {
		t.Fatal(err)
	}
	if code, _ := checkJSON(t, path); code != 0 {
		t.Fatal("repaired check failed")
	}
	if err := os.WriteFile(path, []byte("{\n!}"), 0600); err != nil {
		t.Fatal(err)
	}
	code, issues = checkJSON(t, path)
	if code != 1 || issues[0].Code != "MUXLIO_INVALID_JSON" || issues[0].Source.Line != 2 {
		t.Fatalf("syntax diagnostic: %+v", issues)
	}
	code, issues = checkJSON(t, path+".missing")
	if code != 1 || len(issues) != 1 || issues[0].ID == "" {
		t.Fatal("missing file failure is not structured")
	}
}

func TestTestJSONSetupFailures(t *testing.T) {
	dir := t.TempDir()
	workflow := filepath.Join(dir, "plan.json")
	input := filepath.Join(dir, "input.json")
	config := filepath.Join(dir, "fake.json")
	raw := `{"irVersion":"1.0","name":"setup","root":"root","subgraphs":{"root":{"nodes":[]}}}`
	for _, tc := range []struct{ name, workflow, input, config string }{
		{name: "missing plan", workflow: "", input: `{}`, config: `{}`},
		{name: "invalid plan", workflow: `{!}`, input: `{}`, config: `{}`},
		{name: "missing input", workflow: raw, config: `{}`},
		{name: "invalid input", workflow: raw, input: `{!}`, config: `{}`},
		{name: "invalid config", workflow: raw, input: `{}`, config: `{!}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for path, data := range map[string]string{workflow: tc.workflow, input: tc.input, config: tc.config} {
				_ = os.Remove(path)
				if data != "" {
					if err := os.WriteFile(path, []byte(data), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			old, r, w := captureStdout(t)
			code := cmdTest([]string{workflow, "--input", input, "--fake-config", config, "--json"}, false)
			_ = w.Close()
			restoreStdout(old)
			b, _ := io.ReadAll(r)
			_ = r.Close()
			var body struct {
				Valid  bool                    `json:"valid"`
				Errors []diagnostic.Diagnostic `json:"errors"`
			}
			if err := json.Unmarshal(b, &body); err != nil || code != 1 || body.Valid || len(body.Errors) != 1 || body.Errors[0].ID == "" || body.Errors[0].Fix == "" {
				t.Fatalf("setup failure is not actionable JSON: code=%d body=%s err=%v", code, b, err)
			}
		})
	}
}

func TestBuildAndTestAllowWarnings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plan.json")
	input := filepath.Join(dir, "input.json")
	raw := `{"irVersion":"1.0","name":"warnings","root":"root","subgraphs":{"root":{"nodes":[{"id":"work","kind":"execute","executor":"generic/task","fallbackExecutors":["generic/task"]}]}}}`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"build", "test"} {
		t.Run(command, func(t *testing.T) {
			old, r, w := captureStdout(t)
			var code int
			if command == "build" {
				code = cmdBuild([]string{path, "--json"}, false)
			} else {
				code = cmdTest([]string{path, "--input", input, "--json"}, false)
			}
			_ = w.Close()
			restoreStdout(old)
			b, _ := io.ReadAll(r)
			_ = r.Close()
			var body struct {
				Warnings []diagnostic.Diagnostic `json:"warnings"`
			}
			if err := json.Unmarshal(b, &body); err != nil || code != 0 || len(body.Warnings) != 1 {
				t.Fatalf("warning rejected or lost: code=%d body=%s err=%v", code, b, err)
			}
		})
	}
}
