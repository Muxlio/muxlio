package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/muxlio/muxlio-core/internal/authoring"
	"github.com/muxlio/muxlio-core/internal/plan"
)

func TestCatalogDiscoveryInstantiationAndSubmission(t *testing.T) {
	t.Setenv("PATH", "")
	t.Setenv("MUXLIO_TRUST_WORKFLOW_CODE", "")
	s := &server{}
	listed := s.dispatchTool("list_workflow_templates", json.RawMessage(`{}`))
	var list struct {
		Templates []authoring.WorkflowTemplate `json:"templates"`
	}
	if listed.IsError || json.Unmarshal([]byte(listed.Content[0].Text), &list) != nil || len(list.Templates) != 5 {
		t.Fatalf("discovery: %+v", listed)
	}
	for _, template := range list.Templates {
		args, _ := json.Marshal(map[string]any{"name": template.Name, "version": template.Version})
		inspected := s.dispatchTool("get_workflow_template", args)
		var got authoring.WorkflowTemplate
		if inspected.IsError || json.Unmarshal([]byte(inspected.Content[0].Text), &got) != nil || string(got.ParameterSchema) != string(template.ParameterSchema) {
			t.Fatalf("inspection: %+v", inspected)
		}
	}
	for _, executor := range []string{"generic/first", "generic/second", "generic/third"} {
		t.Run(executor, func(t *testing.T) {
			args, _ := json.Marshal(map[string]any{"name": "sequential-verification", "version": "1", "parameters": map[string]any{
				"work":   map[string]any{"executor": executor, "task": "Summarize the exhibition guide."},
				"verify": map[string]any{"executor": "generic/check", "task": "Verify dates against the supplied source."},
			}})
			result := s.dispatchTool("instantiate_workflow_template", args)
			var instance struct {
				Plan   plan.Plan `json:"plan"`
				Digest string    `json:"digest"`
			}
			if result.IsError || json.Unmarshal([]byte(result.Content[0].Text), &instance) != nil {
				t.Fatalf("instantiate: %+v", result)
			}
			if instance.Digest == "" || instance.Digest != instance.Plan.Digest || instance.Plan.VerifyDigest() != nil || instance.Plan.Subgraphs["root"].Nodes[0].Executor != executor {
				t.Fatal("missing visible plan/digest/executor")
			}
			checkArgs, _ := json.Marshal(map[string]any{"plan": instance.Plan})
			if checked := s.dispatchTool("check_plan", checkArgs); checked.IsError {
				t.Fatal(checked)
			}
			requests := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Plan  plan.Plan      `json:"plan"`
					Input map[string]any `json:"input"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body.Plan.Digest != instance.Digest || body.Plan.VerifyDigest() != nil {
					t.Error("template definition changed in submission")
				}
				switch r.URL.Path {
				case "/v1/workflow-versions":
				case "/v1/runs/preflight", "/v1/runs":
					if body.Input["request"] == nil {
						t.Error("missing separate run input")
					}
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				requests++
				_, _ = w.Write([]byte(`{"feasible":true,"runId":"template-run"}`))
			}))
			defer upstream.Close()
			client := &server{api: newAPIClient(upstream.URL)}
			if registered := client.dispatchTool("register_workflow_version", checkArgs); registered.IsError {
				t.Fatal(registered)
			}
			for _, input := range []string{"Museum guide", "Oral history archive"} {
				submitArgs, _ := json.Marshal(map[string]any{"plan": instance.Plan, "input": map[string]any{"request": input}})
				for _, tool := range []string{"preflight_run", "start_run"} {
					if submitted := client.dispatchTool(tool, submitArgs); submitted.IsError {
						t.Fatal(submitted)
					}
				}
			}
			if requests != 5 {
				t.Fatalf("requests: %d", requests)
			}
		})
	}
}

func TestCatalogToolValidationAndAnnotations(t *testing.T) {
	for _, tc := range []struct{ name, args string }{
		{"list_workflow_templates", `null`},
		{"list_workflow_templates", `{"name":""}`},
		{"get_workflow_template", `{"name":"command-pipeline"}`},
		{"get_workflow_template", `{"name":"command-pipeline","version":"1","parameters":{}}`},
		{"get_workflow_template", `{"name":"missing","version":"1"}`},
		{"instantiate_workflow_template", `{"name":"command-pipeline","version":"2","parameters":{"steps":[]}}`},
		{"instantiate_workflow_template", `{"name":"command-pipeline","version":"1","parameters":{"steps":[]}}`},
		{"instantiate_workflow_template", `{"name":"command-pipeline","version":"1","parameters":{}} {}`},
	} {
		if result := (&server{}).dispatchTool(tc.name, json.RawMessage(tc.args)); !result.IsError {
			t.Errorf("accepted %s %s", tc.name, tc.args)
		}
	}
	for _, tool := range tools {
		if tool.Name == "list_workflow_templates" || tool.Name == "get_workflow_template" || tool.Name == "instantiate_workflow_template" {
			if tool.Annotations == nil || !tool.Annotations.ReadOnly || !tool.Annotations.Idempotent || tool.Annotations.OpenWorld || tool.Annotations.Destructive {
				t.Errorf("unsafe annotations: %+v", tool)
			}
		}
	}
}
