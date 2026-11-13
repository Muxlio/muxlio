package sqlite

import (
	"bytes"
	"testing"

	"github.com/muxlio/muxlio-core/internal/state"
)

func TestRunWorkspaceMetadataAndLegacyFallback(t *testing.T) {
	st := openTemp(t)
	raw := []byte(`{"workspaceId":"plan-alias"}`)
	for _, tc := range []struct{ id, workspace string }{{"registered", "canonical"}, {"legacy", ""}} {
		run := &state.Run{ID: tc.id, WorkflowName: "workspace", PlanJSON: raw, WorkspaceID: tc.workspace}
		if err := st.CreateRun(run); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ id, workspace string }{{"registered", "canonical"}, {"legacy", "plan-alias"}} {
		run, err := st.GetRun(tc.id)
		if err != nil || run.WorkspaceID != tc.workspace || !bytes.Equal(run.PlanJSON, raw) {
			t.Fatalf("get: %+v %v", run, err)
		}
		header, err := st.getRun(tc.id, false)
		if err != nil || header.WorkspaceID != tc.workspace {
			t.Fatalf("header: %+v %v", header, err)
		}
	}
	list, err := st.ListRuns()
	if err != nil {
		t.Fatal(err)
	}
	page, _, _, err := st.ListRunPage(0, 10, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, runs := range [][]*state.Run{list, page} {
		if len(runs) != 2 {
			t.Fatalf("rows=%d", len(runs))
		}
		for _, run := range runs {
			want := "canonical"
			if run.ID == "legacy" {
				want = "plan-alias"
			}
			if run.WorkspaceID != want {
				t.Fatalf("list workspace: %+v", run)
			}
		}
	}
}
