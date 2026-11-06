package sqlite

import (
	"encoding/json"
	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
	"path/filepath"
	"testing"
	"time"
)

func TestOperationReferenceCheckpointSurvivesWorkerAndStoreLoss(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRun(&state.Run{ID: "run"}); err != nil {
		t.Fatal(err)
	}
	a := state.ExecutionAttempt{ID: "attempt", RunID: "run", ActivationID: "act", Executor: "test", RetryNumber: 1, Effect: "reconcilable", State: "running", StartedAt: time.Now()}
	if err := s.RecordExecutionAttempt(a); err != nil {
		t.Fatal(err)
	}
	assignment := state.Assignment{RunID: "run", ActivationID: "act", AttemptID: "attempt", AssignmentID: "slot", WorkerID: "worker", LeaseUntil: time.Now().Add(time.Hour)}
	assignment.Generation, err = s.ClaimAssignment(assignment)
	if err != nil {
		t.Fatal(err)
	}
	chunk := state.LogChunk{RunID: "run", ActivationID: "act", Stream: "operation", Seq: 1, Data: []byte(`{"id":"external-123","url":"https://example.test/123"}`), Timestamp: time.Now()}
	if ok, err := s.AppendWorkerLogChunk(assignment, chunk); err != nil || !ok {
		t.Fatalf("checkpoint: %v %v", ok, err)
	}
	// Final coordinator bookkeeping without a reference must preserve the checkpoint.
	a.State = "awaiting_reconciliation"
	a.AwaitReconciliationReason = "worker lost after external submission"
	if err := s.RecordExecutionAttempt(a); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	attempts, err := s.ListExecutionAttempts("run", "act")
	if err != nil || len(attempts) != 1 || attempts[0].OperationRef == nil || attempts[0].OperationRef.ID != "external-123" {
		t.Fatalf("lost checkpoint: %+v %v", attempts, err)
	}
	if err := s.ResolveExecutionAttempt("run", "attempt", state.AttemptResolution{Outcome: "not-applied", Reason: "adapter confirmed it cannot commit"}); err != nil {
		t.Fatal(err)
	}
	chunk.Seq = 2
	chunk.Data = []byte(`{"id":"late"}`)
	if ok, err := s.AppendWorkerLogChunk(assignment, chunk); err != nil || ok {
		t.Fatalf("lost worker published after resolution: %v %v", ok, err)
	}
	if ok, err := s.CompleteWorkerAssignment(assignment, "succeeded", time.Now(), []byte(`{}`)); err != nil || ok {
		t.Fatalf("lost worker completed after resolution: %v %v", ok, err)
	}
}

func TestIdempotentWorkerRetryKeepsExactRequest(t *testing.T) {
	s := openTemp(t)
	epoch, err := s.StartCoordinator()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRun(&state.Run{ID: "run"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterWorker(state.Worker{ID: "worker", InstanceID: "first", Capacity: 1, LastSeen: time.Now(), Capabilities: []state.WorkerCapability{{Name: "test", Contract: &contract.AdapterManifest{Capabilities: map[string]bool{contract.CapabilityIdempotencyKey: true}}}}}); err != nil {
		t.Fatal(err)
	}
	req := workers.ExecuteRequest{Executor: "test", Effect: "idempotent-with-key", IdempotencyKey: "invoice-123", RunID: "run", ActivationID: "act", AttemptID: "attempt"}
	raw, _ := json.Marshal(req)
	if err := s.EnqueueDispatch(state.Dispatch{AssignmentID: "slot", RunID: "run", ActivationID: "act", AttemptID: "attempt", Executor: "test", Request: raw}); err != nil {
		t.Fatal(err)
	}
	first, g, err := s.ClaimDispatchForEpoch("worker", time.Now().Add(-time.Second), epoch)
	if err != nil || first == nil {
		t.Fatalf("first claim: %v %v", first, err)
	}
	if err := s.RecoverWorkerLeases(); err != nil {
		t.Fatal(err)
	}
	retry, next, err := s.ClaimDispatchForEpoch("worker", time.Now().Add(time.Hour), epoch)
	if err != nil || retry == nil || next <= g || string(retry.Request) != string(raw) {
		t.Fatalf("retry changed request: %+v %v", retry, err)
	}
	var got workers.ExecuteRequest
	if err := json.Unmarshal(retry.Request, &got); err != nil || got.IdempotencyKey != "invoice-123" {
		t.Fatalf("key lost: %+v %v", got, err)
	}
}
