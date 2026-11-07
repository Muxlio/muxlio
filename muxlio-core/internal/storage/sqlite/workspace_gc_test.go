package sqlite

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/artifacts"
	"github.com/muxlio/muxlio-core/internal/runtimefiles"
	"github.com/muxlio/muxlio-core/internal/state"
)

func TestWorkspaceRetentionProtectsLivePinnedAndRecentRuns(t *testing.T) {
	root := t.TempDir()
	s, err := Open(filepath.Join(root, "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	local, err := artifacts.NewLocal(filepath.Join(root, "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	workspaces := filepath.Join(root, "runs")
	now := time.Now().UTC()
	for _, id := range []string{"expired", "pinned", "recent", "active", "uncertain"} {
		if err = s.CreateRun(&state.Run{ID: id, State: "succeeded"}); err != nil {
			t.Fatal(err)
		}
		if _, err = s.db.Exec(`UPDATE runs SET state='succeeded',updated_at=? WHERE id=?`, rfc3339(now.Add(-48*time.Hour)), id); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(workspaces, runtimefiles.PathPart(id))
		if err = os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(path, "scratch.txt"), []byte("temporary workspace"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.PinRetention("run", "pinned", true); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE runs SET updated_at=? WHERE id='recent'`, rfc3339(now.Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE runs SET state='running' WHERE id='active'`); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordExecutionAttempt(state.ExecutionAttempt{ID: "pending", RunID: "uncertain", ActivationID: "a", State: "running"}); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "important")
	if err = os.WriteFile(outside, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, filepath.Join(workspaces, "expired", "external")); err != nil {
		t.Fatal(err)
	}
	policy := state.DefaultRetention()
	cache := filepath.Join(workspaces, "expired", "readonly-cache")
	if err = os.Mkdir(cache, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(cache, "entry"), []byte("cached"), 0400); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(cache, 0500); err != nil {
		t.Fatal(err)
	}
	report, err := s.CollectGarbage(local, policy, now, "", workspaces)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Workspaces) != 1 || report.Workspaces[0].RunID != "expired" {
		t.Fatalf("wrong workspace candidates: %+v", report.Workspaces)
	}
	if _, err = s.CollectIdleGarbage(local, policy, now, "", workspaces); err != nil {
		t.Fatal(err)
	} else if _, err = os.Stat(filepath.Join(workspaces, "expired")); err != nil {
		t.Fatal("idle preview deleted data")
	}
	applied, err := s.CollectGarbage(local, policy, now, report.Token, workspaces)
	if err != nil || !applied.Applied {
		t.Fatalf("apply: %+v %v", applied, err)
	}
	if _, err = os.Stat(filepath.Join(workspaces, "expired")); !os.IsNotExist(err) {
		t.Fatal("expired workspace retained")
	}
	for _, id := range []string{"pinned", "recent", "active", "uncertain"} {
		if _, err = os.Stat(filepath.Join(workspaces, id, "scratch.txt")); err != nil {
			t.Fatal("protected workspace removed", id, err)
		}
	}
	if raw, err := os.ReadFile(outside); err != nil || string(raw) != "keep" {
		t.Fatal("cleanup followed a symlink")
	}
}

func TestWorkspacePreviewRejectsChangesAndSymlinkRoot(t *testing.T) {
	root := t.TempDir()
	s, err := Open(filepath.Join(root, "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	local, _ := artifacts.NewLocal(filepath.Join(root, "artifacts"))
	now := time.Now().UTC()
	if err = s.CreateRun(&state.Run{ID: "old", State: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE runs SET state='succeeded',updated_at=?`, rfc3339(now.Add(-48*time.Hour))); err != nil {
		t.Fatal(err)
	}
	workspaces := filepath.Join(root, "runs")
	file := filepath.Join(workspaces, "old", "file")
	if err = os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(file, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	report, err := s.CollectGarbage(local, state.DefaultRetention(), now, "", workspaces)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(file, []byte("changed after preview"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CollectGarbage(local, state.DefaultRetention(), now, report.Token, workspaces); err == nil {
		t.Fatal("changed workspace was deleted")
	}
	alias := filepath.Join(root, "alias")
	if err = os.Symlink(workspaces, alias); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CollectGarbage(local, state.DefaultRetention(), now, "", alias); err == nil {
		t.Fatal("symlink workspace root accepted")
	}
}
