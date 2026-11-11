package state

import (
	"github.com/muxlio/muxlio-core/internal/testaccounting"
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

func TestExecutionDurationOptionalAndMeasuredZero(t *testing.T) {
	s := New()
	if err := s.CreateRun(&Run{ID: "timing"}); err != nil {
		t.Fatal(err)
	}
	zero := int64(0)
	if err := s.RecordExecutionAttempt(ExecutionAttempt{ID: "old", RunID: "timing", ActivationID: "a", RetryNumber: 1, State: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordExecutionAttempt(ExecutionAttempt{ID: "zero", RunID: "timing", ActivationID: "b", RetryNumber: 1, State: "failed", ExecutionDurationMS: &zero}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListExecutionAttempts("timing", "")
	if err != nil || len(got) != 2 {
		t.Fatalf("attempts=%+v err=%v", got, err)
	}
	if got[0].ExecutionDurationMS != nil || got[1].ExecutionDurationMS == nil || *got[1].ExecutionDurationMS != 0 {
		t.Fatalf("optional timing lost: %+v", got)
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
	reported := testaccounting.Tokens(1200, 800, 300)
	if err := s.RecordExecutionAttempt(ExecutionAttempt{ID: "au-1", RunID: "ru", ActivationID: "au", RetryNumber: 1, Executor: "agent/x", State: "succeeded", StartedAt: time.Now(), FinishedAt: time.Now(), Accounting: reported}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListExecutionAttempts("ru", "au")
	if err != nil || len(got) != 1 || got[0].Accounting == nil {
		t.Fatalf("expected non-nil usage, got=%+v err=%v", got, err)
	}
	if testaccounting.TokenValue(got[0].Accounting, "input") != 1200 || testaccounting.TokenValue(got[0].Accounting, "output") != 800 || testaccounting.TokenValue(got[0].Accounting, "cached") != 300 {
		t.Fatalf("usage mismatch: %+v", got[0].Accounting)
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
		if a.ID == "au-2" && a.Accounting != nil {
			t.Fatalf("missing usage should be nil, not zero: %+v", a.Accounting)
		}
	}

	// Reported zero is distinct from nil.
	if err := s.RecordExecutionAttempt(ExecutionAttempt{ID: "au-3", RunID: "ru", ActivationID: "au", RetryNumber: 3, Executor: "agent/x", State: "succeeded", StartedAt: time.Now(), FinishedAt: time.Now(), Accounting: testaccounting.Tokens(0, 0, 0)}); err != nil {
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
	if zeroAttempt == nil || zeroAttempt.Accounting == nil {
		t.Fatalf("reported zero should be non-nil: %+v", zeroAttempt)
	}
	if testaccounting.TokenValue(zeroAttempt.Accounting, "input") != 0 || testaccounting.TokenValue(zeroAttempt.Accounting, "output") != 0 || testaccounting.TokenValue(zeroAttempt.Accounting, "cached") != 0 {
		t.Fatalf("reported zero should have zero fields: %+v", zeroAttempt.Accounting)
	}
}

func TestRecordExecutionAttemptPreservesObservationsOnReplay(t *testing.T) {
	s := New()
	if err := s.CreateRun(&Run{ID: "r"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordExecutionAttempt(ExecutionAttempt{
		ID: "a-1", RunID: "r", ActivationID: "act", State: "running",
		StartedAt: time.Now(), Accounting: testaccounting.Tokens(10, 5, 0),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordExecutionAttempt(ExecutionAttempt{
		ID: "a-1", RunID: "r", ActivationID: "act", State: "succeeded",
		StartedAt: time.Now(), FinishedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	attempts, err := s.ListExecutionAttempts("r", "act")
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts=%v err=%v", attempts, err)
	}
	if attempts[0].Accounting == nil || testaccounting.TokenValue(attempts[0].Accounting, "input") != 10 {
		t.Fatalf("usage lost on replay: %+v", attempts[0].Accounting)
	}
}

func TestMemoryTimingRetainedOnReplayAndEmitted(t *testing.T) {
	s := New()
	ms := int64(123)
	a := ExecutionAttempt{ID: "attempt", RunID: "r", ExecutionDurationMS: &ms}
	if err := s.RecordExecutionAttempt(a); err != nil {
		t.Fatal(err)
	}
	a.ExecutionDurationMS = nil
	if err := s.RecordExecutionAttempt(a); err != nil {
		t.Fatal(err)
	}
	attempts, _ := s.ListExecutionAttempts("r", "")
	if len(attempts) != 1 || attempts[0].ExecutionDurationMS == nil || *attempts[0].ExecutionDurationMS != 123 {
		t.Fatalf("replay lost timing: %+v", attempts)
	}
	events, _ := s.GetEvents("r", 0, 0)
	if events[len(events)-1].Payload["executionDurationMs"] != ms {
		t.Fatalf("timing not emitted: %+v", events)
	}
}
