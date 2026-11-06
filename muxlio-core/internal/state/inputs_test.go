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
