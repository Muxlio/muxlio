package workers

import "testing"

func TestScriptPreservesReturnedJSONObjectsAndArtifacts(t *testing.T) {
	s := testScript(t, `printf '{"outputs":{"value":{"data":"true","ok":false},"file":{"mime":"application/json","path":"report.json","data":"true"}}}'`)
	r, err := s.Execute(ExecuteRequest{Executor: "test", Outputs: map[string]OutputDecl{
		"value": {Mime: "application/json"}, "file": {Path: "report.json", Mime: "application/json"},
	}})
	if err != nil || r.Error != "" {
		t.Fatalf("result=%+v err=%v", r, err)
	}
	if value, ok := r.Outputs["value"].(map[string]any); !ok || value["ok"] != false || value["data"] != "true" {
		t.Fatalf("JSON result was rewritten: %+v", r)
	}
	if file, ok := r.Outputs["file"].(map[string]any); !ok || file["path"] != "report.json" || file["data"] != "true" {
		t.Fatalf("artifact was decoded as a result: %+v", r)
	}
}

func TestScriptCollectedPortsSeparateJSONFromArtifacts(t *testing.T) {
	s := testScript(t, `printf 'true' > report.json
printf 'false' > approval
printf 'looks good' > message
printf '{}'`)
	r, err := s.Execute(ExecuteRequest{Executor: "test", Outputs: map[string]OutputDecl{
		"file": {Path: "report.json", Mime: "application/json"}, "approval": {}, "message": {},
	}})
	if err != nil || r.Error != "" {
		t.Fatalf("result=%+v err=%v", r, err)
	}
	if file, ok := r.Outputs["file"].(map[string]any); !ok || file["data"] != "true" {
		t.Fatalf("artifact lost: %+v", r)
	}
	if r.Outputs["approval"] != false || r.OutputErrors["approval"] != "" {
		t.Fatalf("JSON port not decoded: %+v", r)
	}
	if r.Outputs["message"] != "looks good" || r.OutputErrors["message"] == "" {
		t.Fatalf("prose not flagged: %+v", r)
	}
}
