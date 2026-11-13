package state_test

import (
	"path/filepath"
	"testing"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
)

func TestAttentionEventsOnlyOnNewWait(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			var s state.Store = state.New()
			if backend == "sqlite" {
				db, err := sqlite.Open(filepath.Join(t.TempDir(), "events.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				s = db
			}
			check := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			check(s.CreateRun(&state.Run{ID: "run", State: "running"}))
			req := &state.InputRequest{RunID: "run", RequestID: "request", Name: "private prompt"}
			check(s.CreateInputRequest(req))
			check(s.CreateInputRequest(req))
			attempt := state.ExecutionAttempt{ID: "attempt", RunID: "run", State: "awaiting_reconciliation", AwaitReconciliationReason: "private reason"}
			check(s.RecordExecutionAttempt(attempt))
			check(s.RecordExecutionAttempt(attempt))
			attempt.AwaitReconciliationReason = ""
			check(s.RecordExecutionAttempt(attempt)) // replay preserves the original wait
			events, err := s.GetEvents("run", 0, 100)
			check(err)
			counts := map[string]int{}
			for _, event := range events {
				counts[event.Type]++
				if event.Type == state.EventInputRequired || event.Type == state.EventReconciliationRequired {
					if len(event.Payload) != 1 {
						t.Fatalf("attention event includes content: %+v", event)
					}
				}
			}
			if counts[state.EventInputRequired] != 1 || counts[state.EventReconciliationRequired] != 1 {
				t.Fatalf("events=%+v", counts)
			}
			// A reconciliation state alone still requires attention, and adding
			// a reason later must not create a second notification.
			other := state.ExecutionAttempt{ID: "other", RunID: "run", State: "awaiting_reconciliation"}
			check(s.RecordExecutionAttempt(other))
			other.AwaitReconciliationReason = "reason added later"
			check(s.RecordExecutionAttempt(other))
			events, err = s.GetEvents("run", 0, 100)
			check(err)
			reconciliations := 0
			for _, event := range events {
				if event.Type == state.EventReconciliationRequired {
					reconciliations++
				}
			}
			if reconciliations != 2 {
				t.Fatalf("reconciliation count=%d", reconciliations)
			}
			check(s.CreateRun(&state.Run{ID: "cancelled"}))
			check(s.CreateActivation(&state.Activation{ID: "wait", RunID: "cancelled", NodeID: "wait", State: "waiting"}))
			check(s.SetRunState("cancelled", "cancelled"))
			check(s.CreateInputRequest(&state.InputRequest{RunID: "cancelled", RequestID: "late", ActivationID: "wait"}))
			events, err = s.GetEvents("cancelled", 0, 100)
			check(err)
			for _, event := range events {
				if event.Type == state.EventInputRequired {
					t.Fatal("cancelled input emitted a notification")
				}
			}
			check(s.RetryRun("cancelled"))
			events, err = s.GetEvents("cancelled", 0, 100)
			check(err)
			renewed := 0
			for _, event := range events {
				if event.Type == state.EventInputRequired {
					renewed++
					if event.Payload["requestId"] != state.RetriedInputRequestID("late") {
						t.Fatalf("wrong renewed identity: %+v", event)
					}
				}
			}
			if renewed != 1 {
				t.Fatalf("renewed notifications=%d", renewed)
			}

		})
	}
}
