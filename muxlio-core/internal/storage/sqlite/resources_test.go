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
	s := openTemp(t)
	admissionWorker(t, s, "worker", 10)
	admissionRun(t, s, "run", 2)
	if _, err := s.db.Exec(`UPDATE dispatches SET request_json='{"concurrencyKeys":["mutable"]}'`); err != nil {
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
