package state_test

import (
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
)

func TestInputRequestDelivery(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			var s state.Store = state.New()
			if backend == "sqlite" {
				db, err := sqlite.Open(filepath.Join(t.TempDir(), "inputs.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				s = db
			}
			if err := s.CreateRun(&state.Run{ID: "r"}); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"a", "b"} {
				if err := s.CreateInputRequest(&state.InputRequest{RunID: "r", RequestID: id, Name: "same"}); err != nil {
					t.Fatal(err)
				}
			}
			var accepted atomic.Int32
			var wg sync.WaitGroup
			for i := 0; i < 20; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					got, ok, err := s.SubmitInputResponse("r", "a", true, "caller", "delivery")
					if err != nil {
						t.Error(err)
						return
					}
					if ok {
						accepted.Add(1)
					}
					if got.Response != true {
						t.Errorf("response: %#v", got.Response)
					}
				}()
			}
			wg.Wait()
			if accepted.Load() != 1 {
				t.Fatalf("accepted %d times", accepted.Load())
			}
			if _, _, err := s.SubmitInputResponse("r", "a", false, "caller", "other"); !errors.Is(err, state.ErrInputRequestNotAccepting) {
				t.Fatalf("conflicting response: %v", err)
			}
			if err := s.CreateInputRequest(&state.InputRequest{RunID: "r", RequestID: "a", Name: "replacement"}); err != nil {
				t.Fatal(err)
			}
			reqs, err := s.ListInputRequests("r")
			if err != nil || len(reqs) != 2 {
				t.Fatalf("requests=%v err=%v", reqs, err)
			}
			b, err := s.GetInputRequest("r", "b")
			if err != nil || b.State != state.InputRequestPending {
				t.Fatalf("other request affected: %v %v", b, err)
			}
			past := time.Now().Add(-time.Second)
			if err := s.CreateInputRequest(&state.InputRequest{RunID: "r", RequestID: "expired", ExpiresAt: &past}); err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.SubmitInputResponse("r", "expired", nil, "caller", "late"); !errors.Is(err, state.ErrInputRequestNotAccepting) {
				t.Fatalf("late response: %v", err)
			}
			if err := s.CancelRunWork("r"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.SubmitInputResponse("r", "b", nil, "caller", "cancelled"); !errors.Is(err, state.ErrInputRequestNotAccepting) {
				t.Fatalf("cancelled response: %v", err)
			}
		})
	}
}

func TestInputRequestSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inputs.db")
	s, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CreateRun(&state.Run{ID: "r"}); err != nil {
		t.Fatal(err)
	}
	if err = s.CreateInputRequest(&state.InputRequest{RunID: "r", RequestID: "request", Name: "review"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.SubmitInputResponse("r", "request", "accepted", "human", "key"); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, accepted, err := s.SubmitInputResponse("r", "request", "different", "human", "key")
	if err != nil || accepted || got.Response != "accepted" || got.ResponderID != "human" {
		t.Fatalf("replay after restart: %+v %v %v", got, accepted, err)
	}
}

func TestInputRequestConcurrentConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inputs.db")
	first, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := first.CreateRun(&state.Run{ID: "r"}); err != nil {
		t.Fatal(err)
	}
	if err := first.CreateInputRequest(&state.InputRequest{RunID: "r", RequestID: "request"}); err != nil {
		t.Fatal(err)
	}
	var accepted atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 20; i++ {
		store := first
		if i%2 != 0 {
			store = second
		}
		wg.Add(1)
		go func(s state.Store) {
			defer wg.Done()
			<-start
			got, ok, err := s.SubmitInputResponse("r", "request", false, "agent", "delivery")
			if err != nil {
				t.Error(err)
				return
			}
			if ok {
				accepted.Add(1)
			}
			if got.Response != false {
				t.Errorf("response = %v", got.Response)
			}
		}(store)
	}
	close(start)
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("accepted %d times across connections", accepted.Load())
	}
}

func TestRetryRenewsCancelledInputs(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			var s state.Store = state.New()
			if backend == "sqlite" {
				db, err := sqlite.Open(filepath.Join(t.TempDir(), "retry.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				s = db
			}
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			must(s.CreateRun(&state.Run{ID: "r"}))
			must(s.CreateActivation(&state.Activation{ID: "ask", NodeID: "ask", RunID: "r", State: "waiting"}))
			must(s.CreateActivation(&state.Activation{ID: "answered", NodeID: "answered", RunID: "r", State: "waiting"}))
			due := time.Now().Add(time.Hour)
			must(s.CreateInputRequest(&state.InputRequest{RunID: "r", RequestID: "pending", ActivationID: "ask", Name: "approval", Schema: []byte(`{"type":"boolean"}`), Permissions: []string{"approve"}, ExpiresAt: &due}))
			must(s.CreateInputRequest(&state.InputRequest{RunID: "r", RequestID: "fulfilled", ActivationID: "answered", Name: "approval"}))
			_, accepted, err := s.SubmitInputResponse("r", "fulfilled", false, "human", "key")
			must(err)
			if !accepted {
				t.Fatal("first response rejected")
			}
			must(s.SetRunState("r", "cancelled"))
			must(s.CancelRunWork("r"))
			must(s.RetryRun("r"))
			requests, err := s.ListInputRequests("r")
			must(err)
			var renewed *state.InputRequest
			for i := range requests {
				if requests[i].State == state.InputRequestPending {
					renewed = &requests[i]
				}
			}
			if renewed == nil || renewed.RequestID == "pending" {
				t.Fatalf("no renewed identity: %+v", requests)
			}
			if renewed.ActivationID != "ask" || renewed.Name != "approval" || string(renewed.Schema) != `{"type":"boolean"}` || len(renewed.Permissions) != 1 || renewed.Permissions[0] != "approve" || renewed.ExpiresAt == nil || !renewed.ExpiresAt.After(due) {
				t.Fatalf("lost request contract: %+v", renewed)
			}
			_, _, err = s.SubmitInputResponse("r", "pending", true, "human", "stale")
			if !errors.Is(err, state.ErrInputRequestNotAccepting) {
				t.Fatalf("old request accepted: %v", err)
			}
			got, accepted, err := s.SubmitInputResponse("r", "fulfilled", true, "human", "key")
			must(err)
			if accepted || got.Response != false {
				t.Fatalf("fulfilled response lost: %+v accepted=%v", got, accepted)
			}
			_, accepted, err = s.SubmitInputResponse("r", renewed.RequestID, true, "human", "new")
			must(err)
			if !accepted {
				t.Fatal("renewed request not answerable")
			}
			_, accepted, err = s.SubmitInputResponse("r", renewed.RequestID, true, "human", "new")
			must(err)
			if accepted {
				t.Fatal("duplicate accepted")
			}
		})
	}
}

// Cancellation must fence first acceptance before the separate work cleanup,
// while responses accepted before cancellation remain replayable. Use separate
// SQLite connections so a process-local mutex cannot satisfy the contract.
func TestInputResponseFencedByRunCancellation(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			var s, canceller state.Store
			s = state.New()
			canceller = s
			if backend == "sqlite" {
				path := filepath.Join(t.TempDir(), "cancel.db")
				first, err := sqlite.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				defer first.Close()
				second, err := sqlite.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				defer second.Close()
				s, canceller = first, second
			}
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			must(s.CreateRun(&state.Run{ID: "r"}))
			must(s.SetRunState("r", "running"))
			for _, id := range []string{"pending", "answered"} {
				must(s.CreateInputRequest(&state.InputRequest{RunID: "r", RequestID: id}))
			}
			_, accepted, err := s.SubmitInputResponse("r", "answered", false, "human", "first")
			must(err)
			if !accepted {
				t.Fatal("first response rejected")
			}
			changed, err := canceller.TransitionRunState("r", []string{"running"}, "cancelled")
			must(err)
			if !changed {
				t.Fatal("cancel rejected")
			}
			// Deliberately do not call CancelRunWork yet.
			_, accepted, err = s.SubmitInputResponse("r", "pending", true, "human", "late")
			if accepted || !errors.Is(err, state.ErrInputRequestNotAccepting) {
				t.Fatalf("accepted after durable cancel: %v %v", accepted, err)
			}
			got, accepted, err := s.SubmitInputResponse("r", "answered", true, "human", "first")
			must(err)
			if accepted || got.Response != false {
				t.Fatalf("lost replay: %+v %v", got, accepted)
			}
			_, _, err = s.SubmitInputResponse("r", "answered", true, "human", "different")
			if !errors.Is(err, state.ErrInputRequestNotAccepting) {
				t.Fatalf("conflicting replay: %v", err)
			}
			must(canceller.CancelRunWork("r"))
			must(s.CreateInputRequest(&state.InputRequest{RunID: "r", RequestID: "late"}))
			got, err = s.GetInputRequest("r", "late")
			must(err)
			if got.State != state.InputRequestCancelled {
				t.Fatalf("late creation left open: %+v", got)
			}
			must(s.CreateInputRequest(&state.InputRequest{RunID: "r", RequestID: "answered"}))
			got, err = s.GetInputRequest("r", "answered")
			must(err)
			if got.State != state.InputRequestFulfilled || got.Response != false {
				t.Fatalf("creation clobbered response: %+v", got)
			}
		})
	}
}
