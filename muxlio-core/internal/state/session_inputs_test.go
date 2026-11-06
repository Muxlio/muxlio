package state_test

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
)

func TestSessionInputQueue(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			var store state.Store = state.New()
			path := filepath.Join(t.TempDir(), "queue.db")
			if backend == "sqlite" {
				db, err := sqlite.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				store = db
			}
			defer func() { store.Close() }()
			if err := store.CreateRun(&state.Run{ID: "run"}); err != nil {
				t.Fatal(err)
			}
			q := &state.SessionQueue{Version: contract.SessionInputVersion, RunID: "run",
				Handle: contract.SessionHandle{ID: "session", ExpiresAt: time.Now().Add(time.Hour)}, Boundary: true, Permissions: []string{"steer"}}
			if ok, err := store.CompareAndSwapSessionQueue(q, 0); err != nil || !ok {
				t.Fatalf("create: %v %v", ok, err)
			}
			input := state.QueuedSessionInput{SessionInput: contract.SessionInput{Version: "1", RequestID: "request", Message: "new direction"}, CallerID: "caller", Policy: "live-or-next-boundary"}
			var wg sync.WaitGroup
			for i := 0; i < 16; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if _, err := state.AcceptSessionInput(store, "run", "session", input); err != nil {
						t.Error(err)
					}
				}()
			}
			wg.Wait()
			got, err := store.GetSessionQueue("run", "session")
			if err != nil || len(got.Inputs) != 1 || got.Inputs[0].Status != "accepted" || got.Inputs[0].Delivery != "next-boundary" || got.Inputs[0].DeliveredAt != nil {
				t.Fatalf("accepted receipt: %+v %v", got, err)
			}
			conflict := input
			conflict.Message = "different"
			if _, err := state.AcceptSessionInput(store, "run", "session", conflict); !errors.Is(err, state.ErrSessionInputConflict) {
				t.Fatalf("conflict: %v", err)
			}
			live := input
			live.RequestID = "live"
			live.Policy = "live-only"
			if _, err := state.AcceptSessionInput(store, "run", "session", live); err == nil {
				t.Fatal("unsupported live steering accepted")
			}
			if backend == "sqlite" {
				store.Close()
				db, err := sqlite.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				store = db
			}
			offered, err := state.BoundaryInputs(store, "run", "session", "attempt")
			if err != nil || len(offered) != 1 {
				t.Fatalf("boundary: %v %v", offered, err)
			}
			if err := state.AcknowledgeSessionInputs(store, "run", "session", offered, []string{"unknown"}); err == nil {
				t.Fatal("accepted false acknowledgment")
			}
			// A failed/unacknowledged turn must leave input available at a later boundary.
			offered, err = state.BoundaryInputs(store, "run", "session", "next-attempt")
			if err != nil || len(offered) != 1 {
				t.Fatalf("fallback: %v %v", offered, err)
			}
			if err := state.AcknowledgeSessionInputs(store, "run", "session", offered, []string{"request"}); err != nil {
				t.Fatal(err)
			}
			if _, err := state.AcceptSessionInput(store, "run", "session", input); err != nil {
				t.Fatal(err)
			}
			got, err = store.GetSessionQueue("run", "session")
			if err != nil || len(got.Inputs) != 1 || got.Inputs[0].Status != "delivered" || got.Inputs[0].DeliveredAt == nil {
				t.Fatalf("delivery receipt: %+v %v", got, err)
			}
			offered, err = state.BoundaryInputs(store, "run", "session", "third")
			if err != nil || len(offered) != 0 {
				t.Fatalf("delivered input replayed: %v %v", offered, err)
			}
		})
	}
}
