package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/muxlio/muxlio-core/internal/state"
)

func TestDiscoveryMigratesLegacyRuns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE runs(id TEXT PRIMARY KEY,workflow_name TEXT NOT NULL,state TEXT NOT NULL,input_json TEXT,output_json TEXT,error TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,updated_at TEXT NOT NULL)`,
		`INSERT INTO runs(id,workflow_name,state,created_at,updated_at) VALUES('legacy','old','succeeded','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	run, err := st.GetRun("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if run.WorkspaceID != "" || run.OriginLabel != "" || run.CallerID != "" {
		t.Fatalf("invented legacy metadata: %+v", run)
	}
	runs, total, _, err := st.ListRunPage(0, 10, "", "", "")
	if err != nil || total != 1 || len(runs) != 1 {
		t.Fatalf("generic discovery: %v %d %v", runs, total, err)
	}
	runs, total, _, err = st.ListRunPage(0, 10, "", "", "", state.RunFilter{WorkspaceID: "project"})
	if err != nil || total != 0 || len(runs) != 0 {
		t.Fatalf("legacy matched workspace: %v %d %v", runs, total, err)
	}
	if err := st.CreateRun(&state.Run{ID: "new", WorkspaceID: "project", SubmissionMetadata: state.SubmissionMetadata{OriginLabel: "Editor", CallerID: "alice"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	runs, total, _, err = st.ListRunPage(0, 10, "", "", "", state.RunFilter{WorkspaceID: "project"})
	if err != nil || total != 1 || len(runs) != 1 || runs[0].OriginLabel != "Editor" {
		t.Fatalf("discovery after migration/reopen: %v %d %v", runs, total, err)
	}
}
