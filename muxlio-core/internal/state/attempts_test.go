package state

import (
	"testing"
	"time"
)

func TestMemoryExecutionAttemptLifecycle(t *testing.T) {
	s := New()
	if err := s.CreateRun(&Run{ID: "r"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateActivation(&Activation{ID: "a", RunID: "r", NodeID: "n", State: "running"}); err != nil {
		t.Fatal(err)
	}
	a := ExecutionAttempt{ID: "a-1", RunID: "r", ActivationID: "a", RetryNumber: 1, Executor: "x", State: "failed", StartedAt: time.Now(), FinishedAt: time.Now(), Failure: "no"}
	active := a
	active.State = "running"
	active.FinishedAt = time.Time{}
	active.Failure = ""
	if err := s.RecordExecutionAttempt(active); err != nil {
		t.Fatal(err)
	}
	ongoing, err := s.ListExecutionAttempts("r", "a")
	if err != nil || len(ongoing) != 1 || ongoing[0].State != "running" || !ongoing[0].FinishedAt.IsZero() {
		t.Fatalf("ongoing=%+v err=%v", ongoing, err)
	}
	if err := s.RecordExecutionAttempt(a); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListExecutionAttempts("r", "a")
	if err != nil || len(got) != 1 || got[0].Failure != "no" {
		t.Fatalf("attempts=%+v err=%v", got, err)
	}
	events, err := s.GetEvents("r", 0, 0)
	if err != nil || events[len(events)-1].Type != EventExecutionAttempt {
		t.Fatalf("events=%+v err=%v", events, err)
	}
}
