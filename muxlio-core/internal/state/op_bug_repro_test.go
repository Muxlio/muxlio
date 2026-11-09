package state

import (
	"testing"
)

// TestOperationStreamInvalidRefReturnsErrorWithoutSideEffect verifies that an
// "operation" stream chunk whose JSON does not contain a non-empty ref ID is
// rejected cleanly: no event must be written and no error must be swallowed.
//
// Before the fix, AppendLogChunk appended the execution.operation event to the
// run's log before checking whether payload["operationRef"] was populated,
// causing a phantom event on the log every time the worker sent a malformed
// operation chunk.
func TestOperationStreamInvalidRefReturnsErrorWithoutSideEffect(t *testing.T) {
	s := New()
	if err := s.CreateRun(&Run{ID: "r"}); err != nil {
		t.Fatal(err)
	}

	// Valid JSON but ref.ID is empty — ProgressEvent will NOT set operationRef.
	c := LogChunk{
		RunID:        "r",
		ActivationID: "a",
		AttemptID:    "a-1",
		Stream:       "operation",
		Seq:          1,
		Data:         []byte(`{}`),
	}
	err := s.AppendLogChunk(c)
	if err == nil {
		t.Fatal("expected error for invalid operation reference, got nil")
	}

	// Completely malformed JSON — ProgressEvent will NOT set operationRef.
	cBadJSON := LogChunk{
		RunID:        "r",
		ActivationID: "a",
		AttemptID:    "a-1",
		Stream:       "operation",
		Seq:          2,
		Data:         []byte(`not-json`),
	}
	err = s.AppendLogChunk(cBadJSON)
	if err == nil {
		t.Fatal("expected error for malformed json in operation reference, got nil")
	}

	// No execution.operation event must have been written.
	events, evErr := s.GetEvents("r", 0, 0)
	if evErr != nil {
		t.Fatal(evErr)
	}
	var opEvents int
	for _, e := range events {
		if e.Type == "execution.operation" {
			opEvents++
		}
	}
	if opEvents != 0 {
		t.Errorf("bug: %d execution.operation event(s) logged even though an error was returned", opEvents)
	}
}

// TestOperationStreamValidRefAppendsEventAndUpdatesAttempt verifies the
// happy path: a well-formed operation chunk writes exactly one event and
// updates the attempt's OperationRef.
func TestOperationStreamValidRefAppendsEventAndUpdatesAttempt(t *testing.T) {
	s := New()
	if err := s.CreateRun(&Run{ID: "r"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordExecutionAttempt(ExecutionAttempt{
		ID: "a-1", RunID: "r", ActivationID: "a", State: "running",
	}); err != nil {
		t.Fatal(err)
	}

	c := LogChunk{
		RunID:        "r",
		ActivationID: "a",
		AttemptID:    "a-1",
		Stream:       "operation",
		Seq:          1,
		Data:         []byte(`{"id":"job-123","url":"https://example.com/job-123"}`),
	}
	if err := s.AppendLogChunk(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	events, _ := s.GetEvents("r", 0, 0)
	var opEvents int
	for _, e := range events {
		if e.Type == "execution.operation" {
			opEvents++
		}
	}
	if opEvents != 1 {
		t.Errorf("expected 1 execution.operation event, got %d", opEvents)
	}

	attempts, _ := s.ListExecutionAttempts("r", "a")
	if len(attempts) != 1 || attempts[0].OperationRef == nil || attempts[0].OperationRef.ID != "job-123" {
		t.Errorf("attempt OperationRef not updated: %+v", attempts)
	}
}

// TestOperationStreamRetryAfterInvalidRefSucceeds verifies that when an invalid
// operation chunk fails, a subsequent retry with the same sequence number is
// not falsely suppressed by duplicate detection and correctly updates the attempt.
func TestOperationStreamRetryAfterInvalidRefSucceeds(t *testing.T) {
	s := New()
	if err := s.CreateRun(&Run{ID: "r"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordExecutionAttempt(ExecutionAttempt{
		ID: "a-1", RunID: "r", ActivationID: "a", State: "running",
	}); err != nil {
		t.Fatal(err)
	}

	// First attempt: invalid chunk (empty ID)
	cInvalid := LogChunk{
		RunID:        "r",
		ActivationID: "a",
		AttemptID:    "a-1",
		Stream:       "operation",
		Seq:          1,
		Data:         []byte(`{}`),
	}
	err := s.AppendLogChunk(cInvalid)
	if err == nil {
		t.Fatal("expected error for invalid operation reference, got nil")
	}

	// Retry: valid chunk with same sequence number
	cValid := LogChunk{
		RunID:        "r",
		ActivationID: "a",
		AttemptID:    "a-1",
		Stream:       "operation",
		Seq:          1,
		Data:         []byte(`{"id":"job-retry","url":"https://example.com/job-retry"}`),
	}
	if err := s.AppendLogChunk(cValid); err != nil {
		t.Fatalf("unexpected error on retry: %v", err)
	}

	events, _ := s.GetEvents("r", 0, 0)
	var opEvents int
	for _, e := range events {
		if e.Type == "execution.operation" {
			opEvents++
		}
	}
	if opEvents != 1 {
		t.Errorf("expected 1 execution.operation event after retry, got %d", opEvents)
	}

	attempts, _ := s.ListExecutionAttempts("r", "a")
	if len(attempts) != 1 || attempts[0].OperationRef == nil || attempts[0].OperationRef.ID != "job-retry" {
		t.Errorf("attempt OperationRef not updated after retry: %+v", attempts)
	}
}

