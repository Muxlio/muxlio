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

func TestMemoryExecutionAttemptUsage(t *testing.T) {
	s := New()
	if err := s.CreateRun(&Run{ID: "ru"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateActivation(&Activation{ID: "au", RunID: "ru", NodeID: "n", State: "running"}); err != nil {
		t.Fatal(err)
	}

	// Reported usage is persisted and round-trips.
	reported := &TokenUsage{Input: 1200, Output: 800, Cached: 300}
	if err := s.RecordExecutionAttempt(ExecutionAttempt{ID: "au-1", RunID: "ru", ActivationID: "au", RetryNumber: 1, Executor: "agent/x", State: "succeeded", StartedAt: time.Now(), FinishedAt: time.Now(), Usage: reported}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListExecutionAttempts("ru", "au")
	if err != nil || len(got) != 1 || got[0].Usage == nil {
		t.Fatalf("expected non-nil usage, got=%+v err=%v", got, err)
	}
	if got[0].Usage.Input != 1200 || got[0].Usage.Output != 800 || got[0].Usage.Cached != 300 {
		t.Fatalf("usage mismatch: %+v", got[0].Usage)
	}

	// Missing usage stays nil, not zero.
	if err := s.RecordExecutionAttempt(ExecutionAttempt{ID: "au-2", RunID: "ru", ActivationID: "au", RetryNumber: 2, Executor: "agent/x", State: "succeeded", StartedAt: time.Now(), FinishedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	got, err = s.ListExecutionAttempts("ru", "au")
	if err != nil || len(got) != 2 {
		t.Fatalf("expected 2 attempts, got=%+v err=%v", got, err)
	}
	for _, a := range got {
		if a.ID == "au-2" && a.Usage != nil {
			t.Fatalf("missing usage should be nil, not zero: %+v", a.Usage)
		}
	}

	// Reported zero is distinct from nil.
	if err := s.RecordExecutionAttempt(ExecutionAttempt{ID: "au-3", RunID: "ru", ActivationID: "au", RetryNumber: 3, Executor: "agent/x", State: "succeeded", StartedAt: time.Now(), FinishedAt: time.Now(), Usage: &TokenUsage{}}); err != nil {
		t.Fatal(err)
	}
	got, err = s.ListExecutionAttempts("ru", "au")
	if err != nil || len(got) != 3 {
		t.Fatalf("expected 3 attempts, got=%+v err=%v", got, err)
	}
	var zeroAttempt *ExecutionAttempt
	for i := range got {
		if got[i].ID == "au-3" {
			zeroAttempt = &got[i]
		}
	}
	if zeroAttempt == nil || zeroAttempt.Usage == nil {
		t.Fatalf("reported zero should be non-nil: %+v", zeroAttempt)
	}
	if zeroAttempt.Usage.Input != 0 || zeroAttempt.Usage.Output != 0 || zeroAttempt.Usage.Cached != 0 {
		t.Fatalf("reported zero should have zero fields: %+v", zeroAttempt.Usage)
	}
}
