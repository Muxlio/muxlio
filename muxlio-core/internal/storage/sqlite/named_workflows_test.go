package sqlite

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/artifacts"
	"github.com/muxlio/muxlio-core/internal/state"
)

func TestNamedWorkflowConcurrentPublicationAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coordinator.db")
	open := func() *Store {
		t.Helper()
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	}
	a, b := open(), open()
	initial := state.WorkflowPublication{Digest: "initial", Name: "first", PlanJSON: []byte(`{"name":"first"}`)}
	first, err := a.PublishWorkflow("flow", 0, initial)
	if err != nil {
		t.Fatal(err)
	}

	// Fail after inserting the plan bytes and moving the pointer inside the transaction.
	if _, err := a.db.Exec(`CREATE TRIGGER fail_publication BEFORE INSERT ON named_workflow_versions
		BEGIN SELECT RAISE(ABORT, 'simulated failure'); END`); err != nil {
		t.Fatal(err)
	}
	broken := state.WorkflowPublication{Digest: "broken", PlanJSON: []byte(`{}`)}
	if _, err := a.PublishWorkflow("flow", 1, broken); err == nil {
		t.Fatal("publication succeeded despite injected failure")
	}
	if got, err := b.GetNamedWorkflow("flow"); err != nil || !reflect.DeepEqual(got, first) {
		t.Fatalf("partially committed publication: %+v, %v", got, err)
	}
	if _, err := b.GetWorkflowVersion(broken.Digest); err == nil {
		t.Fatal("failed publication left registered bytes")
	}
	if _, err := a.db.Exec(`DROP TRIGGER fail_publication`); err != nil {
		t.Fatal(err)
	}

	type result struct {
		digest   string
		workflow state.NamedWorkflow
		err      error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for i, s := range []*Store{a, b} {
		digest := []string{"writer-a", "writer-b"}[i]
		go func() {
			<-start
			publication := state.WorkflowPublication{Digest: digest, Name: digest, PlanJSON: []byte(`{"name":"` + digest + `"}`)}
			got, err := s.PublishWorkflow("flow", 1, publication)
			results <- result{digest, got, err}
		}()
	}
	close(start)
	var winner state.NamedWorkflow
	conflicts := 0
	for range 2 {
		got := <-results
		switch {
		case errors.Is(got.err, state.ErrNamedWorkflowConflict):
			conflicts++
			if _, err := a.GetWorkflowVersion(got.digest); err == nil {
				t.Fatal("conflicting publication left registered bytes")
			}
		case got.err != nil:
			t.Fatal(got.err)
		default:
			winner = got.workflow
			if winner.CurrentDigest != got.digest || winner.CurrentRevision != 2 || len(winner.Versions) != 2 {
				t.Fatalf("winner: %+v", winner)
			}
		}
	}
	if conflicts != 1 {
		t.Fatalf("got %d conflicts, want one", conflicts)
	}

	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	reloaded := open()
	if got, err := reloaded.GetNamedWorkflow("flow"); err != nil || !reflect.DeepEqual(got, winner) {
		t.Fatalf("reload = %+v, %v, want %+v", got, err, winner)
	}
	if err := reloaded.RetireWorkflowVersion(winner.CurrentDigest); !errors.Is(err, state.ErrWorkflowVersionCurrent) {
		t.Fatalf("retire current digest: %v", err)
	}

	if err := reloaded.SaveWorkflowVersion("unreferenced", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	local, err := artifacts.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(2 * time.Hour)
	policy := state.Retention{Caches: time.Hour}
	preview, err := reloaded.CollectGarbage(local, policy, now, "")
	if err != nil || !reflect.DeepEqual(preview.Plans, []string{"unreferenced"}) {
		t.Fatalf("retention preview: %+v, %v", preview, err)
	}
	if _, err := reloaded.CollectGarbage(local, policy, now, preview.Token); err != nil {
		t.Fatal(err)
	}
	switched, err := reloaded.PublishWorkflow("flow", 2, state.WorkflowPublication{Digest: initial.Digest})
	if err != nil || switched.CurrentDigest != initial.Digest || switched.CurrentRevision != 3 ||
		!reflect.DeepEqual(switched.Versions, []state.NamedWorkflowVersion{first.Versions[0], winner.Versions[0]}) {
		t.Fatalf("switch after reload and GC: %+v, %v", switched, err)
	}
}
