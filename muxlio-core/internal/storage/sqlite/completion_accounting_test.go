package sqlite

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
)

func TestWorkerCompletionCommitsAccountingAndReceiptAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	epoch, err := s.StartCoordinator()
	if err != nil {
		t.Fatal(err)
	}
	admissionWorker(t, s, "worker", 1)
	if err := s.CreateRun(&state.Run{ID: "run"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordExecutionAttempt(state.ExecutionAttempt{ID: "attempt", RunID: "run", ActivationID: "act", Executor: "test", State: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueueDispatch(state.Dispatch{AssignmentID: "assignment", AttemptID: "attempt", RunID: "run", ActivationID: "act", Executor: "test", Request: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	d, g, err := s.ClaimDispatchForEpoch("worker", time.Now().Add(time.Hour), epoch)
	if err != nil || d == nil {
		t.Fatalf("claim %v %v", d, err)
	}
	a := state.Assignment{CoordinatorEpoch: epoch, AssignmentID: d.AssignmentID, AttemptID: d.AttemptID, RunID: d.RunID, ActivationID: d.ActivationID, WorkerID: "worker", Generation: g}
	progress := testAccounting("100", "0", "0")
	progress.Final = false
	progress.Revision = 2
	if ok, err := s.RecordWorkerAccountingSnapshot(a, *progress); err != nil || !ok {
		t.Fatalf("progress %v %v", ok, err)
	}
	encode := func(snapshot *contract.Accounting) []byte {
		raw, err := json.Marshal(map[string]any{"accounting": snapshot, "outputs": map[string]any{}})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	conflict := testAccounting("200", "0", "0")
	conflict.Revision = 2
	if ok, err := s.CompleteWorkerAssignment(a, "succeeded", time.Now(), encode(conflict)); err == nil || ok {
		t.Fatal("conflicting completion was acknowledged")
	}
	if receipt, err := s.WorkerResult("attempt"); err != nil || receipt != nil {
		t.Fatalf("rejected completion sealed receipt: %s %v", receipt, err)
	}
	assignments, err := s.ListAssignments("run")
	if err != nil || len(assignments) != 1 || assignments[0].State != "active" {
		t.Fatalf("rejected completion mutated assignment: %+v %v", assignments, err)
	}
	stale := testAccounting("200", "0", "0")
	stale.Revision = 1
	if ok, err := s.CompleteWorkerAssignment(a, "succeeded", time.Now(), encode(stale)); err == nil || ok {
		t.Fatal("stale completion accepted")
	}
	final := testAccounting("200", "0", "0")
	final.Revision = 3
	raw := encode(final)
	if _, err := s.db.Exec(`CREATE TRIGGER reject_receipt BEFORE INSERT ON worker_results BEGIN SELECT RAISE(ABORT,'injected receipt failure'); END`); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.CompleteWorkerAssignment(a, "succeeded", time.Now(), raw); err == nil || ok {
		t.Fatal("receipt failure was acknowledged")
	}
	rowsAfterFailure, err := s.ListExecutionAttempts("run", "")
	if err != nil {
		t.Fatal(err)
	}
	assertAccountingCounts(t, rowsAfterFailure[0].Accounting, "100", "0", "0")
	if rowsAfterFailure[0].Accounting.Final {
		t.Fatal("receipt failure committed final accounting")
	}
	if _, err := s.db.Exec(`DROP TRIGGER reject_receipt`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if ok, err := s.CompleteWorkerAssignment(a, "succeeded", time.Now(), raw); err != nil || !ok {
			t.Fatalf("valid/replayed completion %v %v", ok, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListExecutionAttempts("run", "")
	if err != nil || len(rows) != 1 {
		t.Fatalf("attempts %v %v", rows, err)
	}
	assertAccountingCounts(t, rows[0].Accounting, "200", "0", "0")
	if !rows[0].Accounting.Final || rows[0].Accounting.Revision != 3 {
		t.Fatal("acknowledged accounting not durable")
	}
	if receipt, err := s.WorkerResult("attempt"); err != nil || string(receipt) != string(raw) {
		t.Fatalf("receipt not atomic: %s %v", receipt, err)
	}
	events, err := s.GetEvents("run", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	updates := 0
	for _, e := range events {
		if e.Type == state.EventAccountingUpdated {
			updates++
		}
	}
	if updates != 2 {
		t.Fatalf("conflicts/replays emitted accounting events: %d", updates)
	}
}
