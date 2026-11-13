package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
