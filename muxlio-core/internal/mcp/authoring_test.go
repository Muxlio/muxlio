package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestOfflineTemplateBuildAndSubmit(t *testing.T) {
	t.Setenv("PATH", "")
	t.Setenv("MUXLIO_TRUST_WORKFLOW_CODE", "")
	submitted := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Plan  json.RawMessage `json:"plan"`
			Input json.RawMessage `json:"input"`
		}
		if r.Method != "POST" || r.URL.Path != "/v1/runs" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		issues, _ := validatePlanLocally(body.Plan)
		if len(issues) != 0 || string(body.Input) != `{"message":"hello"}` {
			t.Errorf("invalid submission: %+v %s", issues, body.Input)
		}
		submitted = true
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"runId":"offline-template"}`))
	}))
	defer upstream.Close()
	s := &server{api: newAPIClient(upstream.URL)}
	for _, name := range []string{"echo", "task"} {
		template := s.dispatchTool("workflow_template", json.RawMessage(`{"name":"`+name+`"}`))
		if template.IsError {
			t.Fatal(template)
		}
		path := filepath.Join(t.TempDir(), "workflow.json")
		if err := os.WriteFile(path, []byte(template.Content[0].Text), 0600); err != nil {
			t.Fatal(err)
		}
		args, _ := json.Marshal(map[string]any{"path": path})
		built := s.dispatchTool("build", args)
		var body struct {
			Valid bool            `json:"valid"`
			Plan  json.RawMessage `json:"plan"`
		}
		if err := json.Unmarshal([]byte(built.Content[0].Text), &body); err != nil || built.IsError || !body.Valid {
			t.Fatalf("build: %+v %v", built, err)
		}
		testArgs, _ := json.Marshal(map[string]any{"path": path, "input": map[string]any{"message": "hello"}})
		testRes := s.dispatchTool("test", testArgs)
		if testRes.IsError {
			t.Fatalf("test %s: %+v", name, testRes)
		}
		if name == "echo" {
			args, _ = json.Marshal(map[string]any{"plan": body.Plan, "input": map[string]any{"message": "hello"}, "idempotencyKey": "template-submission"})
			if result := s.dispatchTool("start_run", args); result.IsError {
				t.Fatal(result)
			}
		}
	}
	if !submitted {
		t.Fatal("template was not submitted")
	}
	if result := s.dispatchTool("workflow_template", json.RawMessage(`{"name":"missing"}`)); !result.IsError {
		t.Fatal("accepted unknown template")
	}
}

func TestSourceToolsRequireTrust(t *testing.T) {
	t.Setenv("MUXLIO_TRUST_WORKFLOW_CODE", "")
	t.Setenv("PATH", "")
	for _, name := range []string{"build", "test"} {
		if result := (&server{}).dispatchTool(name, json.RawMessage(`{"path":"untrusted.ts"}`)); !result.IsError {
			t.Fatalf("%s accepted untrusted source", name)
		}
	}
}

func TestSourceCompilationRequiresOperatorAndCallOptIn(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required for trusted source compilation")
	}
	root := t.TempDir()
	runner := filepath.Join(root, "node_modules", "tsx", "dist")
	if err := os.MkdirAll(runner, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(runner, "cli.mjs"), `await import('data:text/javascript,' + encodeURIComponent(process.argv[3]));`)
	write(filepath.Join(runner, "..", "package.json"), `{"version":"4.23.15"}`)
	// This source models credential theft. Neither JSON validation nor a missing
	// trust acknowledgement may execute it, even on a source-enabled MCP host.
	credential := filepath.Join(root, "operator-credential")
	marker := filepath.Join(root, "stolen-credential")
	write(credential, "operator-secret")
	template := (&server{}).dispatchTool("workflow_template", json.RawMessage(`{"name":"echo"}`)).Content[0].Text
	credJSON, _ := json.Marshal(credential)
	markerJSON, _ := json.Marshal(marker)
	source := filepath.Join(root, "workflow.mjs")
	write(source, `import fs from 'node:fs'; fs.writeFileSync(`+string(markerJSON)+`, fs.readFileSync(`+string(credJSON)+`)); export default {plan:`+template+`};`)
	// Resolve Node explicitly while keeping the local runner fixture independent
	// of any package managers or repository dependencies.
	t.Setenv("PATH", filepath.Dir(node))
	s := &server{}
	for _, tool := range []string{"build", "test"} {
		for _, flags := range []struct{ operator, call bool }{{false, false}, {false, true}, {true, false}} {
			if flags.operator {
				t.Setenv("MUXLIO_TRUST_WORKFLOW_CODE", "1")
			} else {
				t.Setenv("MUXLIO_TRUST_WORKFLOW_CODE", "")
			}
			args, _ := json.Marshal(map[string]any{"path": source, "trustedSource": flags.call})
			result := s.dispatchTool(tool, args)
			if !result.IsError || !strings.Contains(result.Content[0].Text, "arbitrary code") {
				t.Fatalf("%s %+v: %+v", tool, flags, result)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("untrusted source executed: %v", err)
			}
		}
	}
	t.Setenv("MUXLIO_TRUST_WORKFLOW_CODE", "1")
	checkArgs, _ := json.Marshal(map[string]any{"plan": json.RawMessage(template), "path": source, "trustedSource": true})
	if result := s.dispatchTool("check_plan", checkArgs); result.IsError {
		t.Fatal(result)
	}
	jsonPath := filepath.Join(root, "workflow.json")
	write(jsonPath, template)
	for _, tool := range []string{"build", "test"} {
		args, _ := json.Marshal(map[string]any{"path": jsonPath})
		if result := s.dispatchTool(tool, args); result.IsError {
			t.Fatal(result)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("JSON validation executed source: %v", err)
	}
	for _, tool := range []string{"build", "test"} {
		args, _ := json.Marshal(map[string]any{"path": source, "trustedSource": true})
		if result := s.dispatchTool(tool, args); result.IsError {
			t.Fatalf("explicit trusted %s: %+v", tool, result)
		}
		if data, err := os.ReadFile(marker); err != nil || string(data) != "operator-secret" {
			t.Fatalf("trusted compilation did not execute fixture: %s %v", data, err)
		}
	}
}
