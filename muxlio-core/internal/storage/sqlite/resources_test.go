package sqlite

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
)

func TestExclusiveResourcesAcrossIndependentStoreConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resources.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	for _, id := range []string{"first", "second"} {
		admissionWorker(t, first, id, 10)
		if err := first.CreateRun(&state.Run{ID: id}); err != nil {
			t.Fatal(err)
		}
		if err := first.EnqueueDispatch(state.Dispatch{AssignmentID: id, ActivationID: id, AttemptID: id, RunID: id, Executor: "test", Request: []byte(`{"concurrencyKeys":["shared"]}`)}); err != nil {
			t.Fatal(err)
		}
	}
	type result struct {
		d   *state.Dispatch
		err error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for id, store := range map[string]*Store{"first": first, "second": second} {
		go func() {
			<-start
			d, _, err := store.ClaimDispatch(id, time.Now().Add(time.Hour))
			results <- result{d, err}
		}()
	}
	close(start)
	holders := 0
	for range 2 {
		r := <-results
		if r.err != nil {
			t.Fatal(r.err)
		}
		if r.d != nil {
			holders++
		}
	}
	if holders != 1 {
		t.Fatalf("independent connections admitted %d holders", holders)
	}
}

func TestExclusiveResourcesAcrossWorkers(t *testing.T) {
	s := openTemp(t)
	for i := range 8 {
		id := fmt.Sprint(i)
		admissionWorker(t, s, id, 10)
		if err := s.CreateRun(&state.Run{ID: id}); err != nil {
			t.Fatal(err)
		}
		if err := s.EnqueueDispatch(state.Dispatch{AssignmentID: id, ActivationID: id, AttemptID: id, RunID: id, Executor: "test", Request: []byte(`{"concurrencyKeys":["repository","external-resource"]}`)}); err != nil {
			t.Fatal(err)
		}
	}
	type claim struct {
		worker     string
		dispatch   *state.Dispatch
		generation int64
		err        error
	}
	results := make(chan claim, 8)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			id := fmt.Sprint(i)
			d, g, err := s.ClaimDispatch(id, time.Now().Add(time.Hour))
			results <- claim{id, d, g, err}
		})
	}
	wg.Wait()
	close(results)
	var holder claim
	count := 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.dispatch != nil {
			count++
			holder = result
		}
	}
	if count != 1 {
		t.Fatalf("overlapping resource holders: %d", count)
	}
	admissionComplete(t, s, holder.worker, holder.dispatch, holder.generation)
	if next, _ := admissionClaim(t, s, "0"); next == nil {
		t.Fatal("completion did not release resource")
	}
}

func TestLostExclusiveHolderRequiresReconciliation(t *testing.T) {
	s := openTemp(t)
	admissionWorker(t, s, "worker", 10)
	admissionRun(t, s, "run", 2)
	if _, err := s.db.Exec(`UPDATE dispatches SET request_json='{"effect":"none","concurrencyKeys":["mutable"]}'`); err != nil {
		t.Fatal(err)
	}
	d, _, err := s.ClaimDispatch("worker", time.Now().Add(-time.Second))
	if err != nil || d == nil {
		t.Fatalf("claim: %v %v", d, err)
	}
	if next, _ := admissionClaim(t, s, "worker"); next != nil {
		t.Fatal("expired resource holder overlapped new attempt")
	}
	var status string
	if err := s.db.QueryRow(`SELECT state FROM assignments WHERE activation_id=?`, d.ActivationID).Scan(&status); err != nil || status != "uncertain" {
		t.Fatalf("lost holder: %s %v", status, err)
	}
}

func TestCancelledExclusiveHolderRetainsResourceUntilCompletion(t *testing.T) {
	for name, raw := range map[string]string{
		"explicit":  `{"concurrencyKeys":["mutable"]}`,
		"workspace": `{"workspaceMode":"shared-write","workspaceId":"project","workspaceWorkerId":"worker"}`,
	} {
		t.Run(name, func(t *testing.T) {
			s := openTemp(t)
			admissionWorker(t, s, "worker", 10)
			admissionRun(t, s, "run", 2)
			if _, err := s.db.Exec(`UPDATE workers SET labels_json=?`, `{"muxlio.dev/workspace-id/project":"true"}`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`UPDATE dispatches SET request_json=?`, raw); err != nil {
				t.Fatal(err)
			}
			d, _ := admissionClaim(t, s, "worker")
			if err := s.CancelAttemptWork(d.AttemptID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`UPDATE assignments SET lease_until=? WHERE attempt_id=?`, rfc3339(time.Now().Add(-time.Hour)), d.AttemptID); err != nil {
				t.Fatal(err)
			}
			if next, _ := admissionClaim(t, s, "worker"); next != nil {
				t.Fatal("cancelled resource holder overlapped after lease expiry")
			}
			a, err := s.LookupWorkerAssignment("worker", d.AssignmentID)
			if err != nil || a == nil {
				t.Fatalf("assignment: %v %v", a, err)
			}
			if ok, err := s.AckWorkerCompletionWithoutResult(*a, "cancelled", a.Generation-1); err != nil || !ok {
				t.Fatalf("completion: %v %v", ok, err)
			}
			if next, _ := admissionClaim(t, s, "worker"); next == nil {
				t.Fatal("confirmed completion did not release resource")
			}
		})
	}
}

func TestIsolationRequiredDispatchCannotClaimNativeWorker(t *testing.T) {
	s := openTemp(t)
	admissionWorker(t, s, "native", 10)
	admissionRun(t, s, "run", 1)
	if _, err := s.db.Exec(`UPDATE dispatches SET request_json='{"params":{"isolation":{"mode":"required","runtime":"bwrap"}}}'`); err != nil {
		t.Fatal(err)
	}
	if d, _ := admissionClaim(t, s, "native"); d != nil {
		t.Fatal("untrusted task admitted on native worker")
	}
}

func TestExclusiveResourceReservationSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resources.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	admissionWorker(t, s, "first", 10)
	admissionWorker(t, s, "second", 10)
	admissionRun(t, s, "run", 2)
	if _, err := s.db.Exec(`UPDATE dispatches SET request_json='{"concurrencyKeys":["repository"]}'`); err != nil {
		t.Fatal(err)
	}
	d, g := admissionClaim(t, s, "first")
	if d == nil {
		t.Fatal("first holder not admitted")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if next, _ := admissionClaim(t, s, "second"); next != nil {
		t.Fatal("reopen lost exclusive reservation")
	}
	admissionComplete(t, s, "first", d, g)
	if next, _ := admissionClaim(t, s, "second"); next == nil {
		t.Fatal("completion did not release persisted reservation")
	}
}

func TestExclusiveKeysReserveTogetherWithoutBlockingUnrelatedResources(t *testing.T) {
	s := openTemp(t)
	admissionWorker(t, s, "worker", 10)
	admissionRun(t, s, "run", 3)
	for id, raw := range map[string]string{
		"run-0": `{"concurrencyKeys":["repository","repository"]}`,
		"run-1": `{"concurrencyKeys":["deployment","repository"]}`,
		"run-2": `{"concurrencyKeys":["deployment"]}`,
	} {
		if _, err := s.db.Exec(`UPDATE dispatches SET request_json=? WHERE attempt_id=?`, raw, id); err != nil {
			t.Fatal(err)
		}
	}
	first, generation := admissionClaim(t, s, "worker")
	if first == nil || first.AttemptID != "run-0" {
		t.Fatalf("first holder: %+v", first)
	}
	unrelated, otherGeneration := admissionClaim(t, s, "worker")
	if unrelated == nil || unrelated.AttemptID != "run-2" {
		t.Fatalf("blocked request reserved part of its keys: %+v", unrelated)
	}
	admissionComplete(t, s, "worker", first, generation)
	if next, _ := admissionClaim(t, s, "worker"); next != nil {
		t.Fatal("second key was not enforced")
	}
	admissionComplete(t, s, "worker", unrelated, otherGeneration)
	if next, _ := admissionClaim(t, s, "worker"); next == nil || next.AttemptID != "run-1" {
		t.Fatalf("released keys unavailable: %+v", next)
	}
}

func TestWorkspaceWritesSurviveLossAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	admissionWorker(t, s, "worker", 10)
	for i, raw := range []string{
		`{"workspaceMode":"shared-write","workspaceId":"project","workspaceWorkerId":"worker","effect":"pure"}`,
		`{"workspaceMode":"shared-write","workspaceId":"project","workspaceWorkerId":"worker","effect":"pure","concurrencyKeys":["client-two"]}`,
		`{"workspaceMode":"shared-write","workspaceId":"independent","workspaceWorkerId":"worker"}`,
		`{"workspaceMode":"snapshot","workspaceId":"project","workspaceWorkerId":"worker"}`,
		`{"workspaceMode":"empty","workspaceId":"project","workspaceWorkerId":"worker"}`,
	} {
		id := fmt.Sprint(i)
		admissionRun(t, s, id, 1)
		if _, err := s.db.Exec(`UPDATE dispatches SET request_json=? WHERE run_id=?`, raw, id); err != nil {
			t.Fatal(err)
		}
	}
	// Advertise both authorized worktrees for generic placement matching.
	if _, err := s.db.Exec(`UPDATE workers SET labels_json=?`, `{"muxlio.dev/workspace-id/project":"true","muxlio.dev/workspace-id/independent":"true"}`); err != nil {
		t.Fatal(err)
	}
	holder, _, err := s.ClaimDispatch("worker", time.Now().Add(-time.Second))
	if err != nil || holder == nil || holder.RunID != "0" {
		t.Fatalf("initial claim: %+v %v", holder, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, want := range []string{"2", "3", "4"} {
		d, g := admissionClaim(t, s, "worker")
		if d == nil || d.RunID != want {
			t.Fatalf("safe independent dispatch %s blocked: %+v", want, d)
		}
		admissionComplete(t, s, "worker", d, g)
	}
	if d, _ := admissionClaim(t, s, "worker"); d != nil {
		t.Fatalf("lost writer replayed or overlapped: %+v", d)
	}
	// A fresh attempt for the same activation cannot bypass an uncertain holder.
	retry := *holder
	retry.AttemptID, retry.AssignmentID = "retry", "retry"
	if err := s.EnqueueDispatch(retry); err != nil {
		t.Fatal(err)
	}
	if d, _ := admissionClaim(t, s, "worker"); d != nil {
		t.Fatalf("retry bypassed uncertain workspace reservation: %+v", d)
	}
	var status, reason string
	if err := s.db.QueryRow(`SELECT state FROM assignments WHERE attempt_id=?`, holder.AttemptID).Scan(&status); err != nil || status != "uncertain" {
		t.Fatalf("lost writer status: %s %v", status, err)
	}
	if err := s.db.QueryRow(`SELECT reason FROM admission_queue WHERE activation_id='1-0'`).Scan(&reason); err != nil || reason != "Waiting for exclusive resource key" {
		t.Fatalf("generic admission reason: %q %v", reason, err)
	}
	// Only a confirmed terminal assignment permits explicit reconciliation.
	if err := s.ReconcileUncertainDispatch(holder.ActivationID); err == nil {
		t.Fatal("accepted reconciliation of nonterminal writer")
	}
	if d, _ := admissionClaim(t, s, "worker"); d != nil {
		t.Fatal("reconciliation released a nonterminal writer")
	}
	if _, err := s.db.Exec(`UPDATE assignments SET state='failed' WHERE activation_id=?`, holder.ActivationID); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileUncertainDispatch(holder.ActivationID); err != nil {
		t.Fatal(err)
	}
	if err := s.CancelAttemptWork(holder.AttemptID); err != nil {
		t.Fatal(err)
	}
	if d, _ := admissionClaim(t, s, "worker"); d == nil || d.RunID != "1" {
		t.Fatalf("safe reconciliation did not release workspace: %+v", d)
	}
}

func TestWorkspaceWritesAtomicAcrossConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	admissionWorker(t, first, "worker", 10)
	if _, err := first.db.Exec(`UPDATE workers SET labels_json=?`, `{"muxlio.dev/workspace-id/project":"true"}`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"client-one", "client-two"} {
		if err := first.CreateRun(&state.Run{ID: id}); err != nil {
			t.Fatal(err)
		}
		raw := fmt.Sprintf(`{"workspaceMode":"shared-write","workspaceId":"project","workspaceWorkerId":"worker","concurrencyKeys":[%q]}`, id)
		if err := first.EnqueueDispatch(state.Dispatch{AssignmentID: id, ActivationID: id, AttemptID: id, RunID: id, Executor: "test", Request: []byte(raw)}); err != nil {
			t.Fatal(err)
		}
	}
	type claim struct {
		d   *state.Dispatch
		g   int64
		err error
	}
	results := make(chan claim, 2)
	start := make(chan struct{})
	for _, store := range []*Store{first, second} {
		go func() {
			<-start
			d, g, err := store.ClaimDispatch("worker", time.Now().Add(time.Hour))
			results <- claim{d, g, err}
		}()
	}
	close(start)
	var holder claim
	count := 0
	for range 2 {
		c := <-results
		if c.err != nil {
			t.Fatal(c.err)
		}
		if c.d != nil {
			count++
			holder = c
		}
	}
	if count != 1 {
		t.Fatalf("overlapping workspace writers: %d", count)
	}
	admissionComplete(t, first, "worker", holder.d, holder.g)
	if d, _ := admissionClaim(t, second, "worker"); d == nil {
		t.Fatal("completion did not release workspace")
	}
}
