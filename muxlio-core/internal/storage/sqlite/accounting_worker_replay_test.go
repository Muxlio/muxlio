package sqlite

import (
	"encoding/json"
	"github.com/muxlio/muxlio-core/internal/state"
	"strings"
	"testing"
	"time"
)

func TestAccountingAfterPhysicalWorkerReexecution(t *testing.T) {
	s, epoch, first := lostWorkerWithAccounting(t)
	d, g, err := s.ClaimDispatchForEpoch("worker", time.Now().Add(time.Hour), epoch)
	if err != nil || d == nil {
		t.Fatalf("reclaim %v %v", d, err)
	}
	second := first
	second.Generation = g
	if ok, _ := s.RecordWorkerAccountingSnapshot(first, *testAccounting("999", "0", "0")); ok {
		t.Fatal("lost worker could rewrite archived consumption")
	}
	fresh := testAccounting("200", "0", "0")
	fresh.Final = false
	if ok, err := s.RecordWorkerAccountingSnapshot(second, *fresh); !ok || err != nil {
		t.Errorf("new worker generation %d cannot publish its recorder revision 1: %v %v", g, ok, err)
	}
	fresh.Revision = 6
	fresh.Final = true
	if ok, err := s.RecordWorkerAccountingSnapshot(second, *fresh); !ok || err != nil {
		t.Fatalf("higher revision %v %v", ok, err)
	}
	rows, err := s.ListExecutionAttempts("run", "")
	if err != nil {
		t.Fatal(err)
	}
	summary, err := state.SummarizeAccounting(rows)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Tokens.KnownSubtotal == nil || *summary.Tokens.KnownSubtotal != "300" {
		t.Errorf("physical reexecution dropped confirmed first-worker consumption: %+v", summary.Tokens)
	}
}

func TestWorkerReplayChecksPinnedLimitsBeforeExecutingAgain(t *testing.T) {
	for _, tc := range []struct{ name, limits, want string }{
		{"work", `{"maxAttempts":1}`, "budget-exhausted"},
		{"accounting", `{"accounting":[{"kind":"tokens","maximum":"100"}]}`, "budget-exhausted"},
		{"incomplete", `{"accounting":[{"kind":"tokens","maximum":"1000"}]}`, "missing-accounting"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, epoch, _ := lostWorkerWithAccounting(t)
			if _, err := s.db.Exec(`UPDATE runs SET plan_json=? WHERE id='run'`, `{"limits":`+tc.limits+`}`); err != nil {
				t.Fatal(err)
			}
			d, _, err := s.ClaimDispatchForEpoch("worker", time.Now().Add(time.Hour), epoch)
			if err != nil || d != nil {
				t.Fatalf("over-budget replay admitted: %v %v", d, err)
			}
			raw, err := s.WorkerResult("attempt")
			if err != nil {
				t.Fatal(err)
			}
			var result struct {
				Error string `json:"error"`
			}
			if json.Unmarshal(raw, &result) != nil || !strings.Contains(result.Error, tc.want) {
				t.Fatalf("waiter did not receive budget failure: %s", raw)
			}
			rows, err := s.ListExecutionAttempts("run", "")
			if err != nil || len(rows) != 1 || rows[0].Accounting.Entries[0].Tokens.Input.Collected.Value != "100" {
				t.Fatalf("blocked replay changed accounting: %+v %v", rows, err)
			}
		})
	}
}

func lostWorkerWithAccounting(t *testing.T) (*Store, int64, state.Assignment) {
	t.Helper()
	s := openTemp(t)
	epoch, err := s.StartCoordinator()
	if err != nil {
		t.Fatal(err)
	}
	admissionWorker(t, s, "worker", 2)
	if err = s.ConfigureAdmission(state.AdmissionConfig{GlobalCap: 2}); err != nil {
		t.Fatal(err)
	}
	if err = s.CreateRun(&state.Run{ID: "run"}); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordExecutionAttempt(state.ExecutionAttempt{ID: "attempt", RunID: "run", ActivationID: "act", Executor: "test", State: "running"}); err != nil {
		t.Fatal(err)
	}
	if err = s.EnqueueDispatch(state.Dispatch{AssignmentID: "assignment", AttemptID: "attempt", ActivationID: "act", RunID: "run", Executor: "test", Request: []byte(`{"effect":"pure"}`)}); err != nil {
		t.Fatal(err)
	}
	d, g, err := s.ClaimDispatchForEpoch("worker", time.Now().Add(time.Hour), epoch)
	if err != nil || d == nil {
		t.Fatalf("claim %v %v", d, err)
	}
	first := state.Assignment{CoordinatorEpoch: epoch, AssignmentID: d.AssignmentID, AttemptID: d.AttemptID, ActivationID: d.ActivationID, RunID: d.RunID, WorkerID: "worker", Generation: g}
	progress := testAccounting("100", "0", "0")
	progress.Final = false
	progress.Revision = 5
	if ok, err := s.RecordWorkerAccountingSnapshot(first, *progress); !ok || err != nil {
		t.Fatalf("initial progress %v %v", ok, err)
	}
	if _, err = s.db.Exec(`UPDATE assignments SET lease_until=?`, leaseTimestamp(time.Now().Add(-time.Second))); err != nil {
		t.Fatal(err)
	}
	if err = s.RecoverWorkerLeases(); err != nil {
		t.Fatal(err)
	}
	return s, epoch, first
}
