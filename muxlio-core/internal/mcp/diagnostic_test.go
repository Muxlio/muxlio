package mcp

import (
	"encoding/json"
	"fmt"
	"github.com/muxlio/muxlio-core/internal/diagnostic"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestToolDiagnosticsAreRedactedJSON(t *testing.T) {
	t.Setenv("TEST_API_TOKEN", "tool-private-credential")
	result := errResult("request failed with tool-private-credential token=private-token")
	var d diagnostic.Diagnostic
	if err := json.Unmarshal([]byte(result.Content[0].Text), &d); err != nil || !result.IsError || d.ID == "" || d.Fix == "" {
		t.Fatalf("unstructured tool error: %+v", result)
	}
	if strings.Contains(result.Content[0].Text, "tool-private-credential") || strings.Contains(result.Content[0].Text, "private-token") {
		t.Fatal("tool error leaked secret")
	}
	result = apiResultOrError(&apiResponse{statusCode: 422, body: []byte(`{"code":"MUXLIO_TEST","message":"Bearer private-token"}`)})
	var v map[string]any
	if err := json.Unmarshal([]byte(result.Content[0].Text), &v); err != nil || v["httpStatus"] != float64(422) || strings.Contains(result.Content[0].Text, "private-token") {
		t.Fatalf("unsafe HTTP forwarding: %+v", result)
	}
}

func TestCheckPlanDiagnosticRepair(t *testing.T) {
	s := &server{}
	raw := `{"plan":{"irVersion":"1.0","name":"repair","root":"root","subgraphs":{"root":{"nodes":[{"id":"work","kind":"execute","executor":"generic/task","inputs":{"x":{"ref":{"node":"missing","port":"x"}}}}]}}}}`
	result := s.toolCheckPlan(json.RawMessage(raw))
	var body struct {
		Valid  bool                    `json:"valid"`
		Errors []diagnostic.Diagnostic `json:"errors"`
	}
	if err := json.Unmarshal([]byte(result.Content[0].Text), &body); err != nil || !result.IsError || len(body.Errors) != 1 || body.Errors[0].Code != "MUXLIO_BAD_REF" || body.Errors[0].Source.Column == 0 {
		t.Fatalf("invalid check diagnostic: %+v err=%v", result, err)
	}
	id := body.Errors[0].ID
	result = s.toolCheckPlan(json.RawMessage(raw))
	if err := json.Unmarshal([]byte(result.Content[0].Text), &body); err != nil || body.Errors[0].ID != id {
		t.Fatal("unstable check ID")
	}
	repaired := strings.Replace(raw, `"ref":{"node":"missing","port":"x"}`, `"expr":{"op":"literal","value":"repaired"}`, 1)
	result = s.toolCheckPlan(json.RawMessage(repaired))
	if err := json.Unmarshal([]byte(result.Content[0].Text), &body); err != nil || result.IsError || !body.Valid {
		t.Fatalf("repair failed: %+v err=%v", result, err)
	}
}

func TestBuildPreservesExecutablePlan(t *testing.T) {
	nodes := make([]map[string]any, 150)
	task := strings.Repeat("long task ", 1000)
	for i := range nodes {
		nodes[i] = map[string]any{"id": fmt.Sprintf("n%d", i), "kind": "execute", "executor": "generic/task", "task": task}
	}
	raw, err := json.Marshal(map[string]any{"irVersion": "1.0", "name": "large plan", "root": "root", "subgraphs": map[string]any{"root": map[string]any{"nodes": nodes}}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": path})
	result := (&server{}).toolBuild(args)
	var body struct {
		Valid bool `json:"valid"`
		Plan  struct {
			Subgraphs map[string]struct {
				Nodes []struct {
					Task string `json:"task"`
				} `json:"nodes"`
			} `json:"subgraphs"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(result.Content[0].Text), &body); err != nil || result.IsError || !body.Valid {
		t.Fatalf("build failed: %v %+v", err, result)
	}
	got := body.Plan.Subgraphs["root"].Nodes
	if len(got) != len(nodes) || got[0].Task != task {
		t.Fatalf("compiled plan silently truncated: nodes=%d", len(got))
	}
}

func TestCheckAndBuildAllowWarnings(t *testing.T) {
	raw := json.RawMessage(`{"irVersion":"1.0","name":"warnings","root":"root","subgraphs":{"root":{"nodes":[{"id":"work","kind":"execute","executor":"generic/task","fallbackExecutors":["generic/task"]}]}}}`)
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"check_plan", "build", "test"} {
		t.Run(tool, func(t *testing.T) {
			args, _ := json.Marshal(map[string]any{"plan": raw, "path": path, "input": map[string]any{}})
			result := (&server{}).dispatchTool(tool, args)
			var body struct {
				Warnings []diagnostic.Diagnostic `json:"warnings"`
			}
			if err := json.Unmarshal([]byte(result.Content[0].Text), &body); err != nil || result.IsError || len(body.Warnings) != 1 {
				t.Fatalf("warning rejected or lost: %+v err=%v", result, err)
			}
		})
	}
}
