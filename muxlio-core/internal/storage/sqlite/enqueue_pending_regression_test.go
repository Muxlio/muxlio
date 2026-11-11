package sqlite

import (
	"testing"
	"time"

	"github.com/muxlio/muxlio-core/internal/state"
)

func TestEnqueueDispatchReplacesPendingAttemptWithNewAttemptID(t *testing.T) {
	s := openTemp(t)
	if err := s.CreateRun(&state.Run{ID: "run", State: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateActivation(&state.Activation{ID: "act", RunID: "run", NodeID: "node", State: "queued"}); err != nil {
		t.Fatal(err)
	}
	first := state.Dispatch{AssignmentID: "as-first", RunID: "run", ActivationID: "act", AttemptID: "first", Executor: "test", Request: []byte(`{}`)}
	if err := s.EnqueueDispatch(first); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordExecutionAttempt(state.ExecutionAttempt{ID: "second", RunID: "run", ActivationID: "act", Executor: "test", State: "queued", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	second := state.Dispatch{AssignmentID: "as-second", RunID: "run", ActivationID: "act", AttemptID: "second", Executor: "test", Request: []byte(`{}`)}
	if err := s.EnqueueDispatch(second); err != nil {
		t.Fatal(err)
	}
	var attemptID string
	if err := s.db.QueryRow(`SELECT attempt_id FROM dispatches WHERE activation_id='act'`).Scan(&attemptID); err != nil {
		t.Fatal(err)
	}
	if attemptID != "second" {
		t.Fatalf("pending dispatch stuck on attempt %q, want second", attemptID)
	}
}
