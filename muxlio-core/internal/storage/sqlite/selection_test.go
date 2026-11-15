package sqlite

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func selectionWorker(t *testing.T, s *Store, id string, capacity int, names ...string) {
	t.Helper()
	var caps []state.WorkerCapability
	for _, name := range names {
		caps = append(caps, state.WorkerCapability{Name: name, Contract: &contract.AdapterManifest{Capabilities: map[string]bool{contract.CapabilityIdempotencyKey: true}}})
	}
	if err := s.RegisterWorker(state.Worker{ID: id, Capacity: capacity, Capabilities: caps, LastSeen: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

func selectionRun(t *testing.T, s *Store) {
	t.Helper()
	if err := s.CreateRun(&state.Run{ID: "run", State: "running"}); err != nil {
		t.Fatal(err)
	}
}

func selectionDispatch(t *testing.T, s *Store, id string, req workers.ExecuteRequest) {
	t.Helper()
	req.RunID, req.AttemptID = "run", id
	if req.ActivationID == "" {
		req.ActivationID = id
	}
	if req.Executor == "" {
		req.Executor = "any"
	}
	req.WorkspaceMode = "empty"
	if _, err := s.GetActivation(req.ActivationID); err != nil {
		if err := s.CreateActivation(&state.Activation{ID: req.ActivationID, RunID: "run", NodeID: req.ActivationID, State: "queued"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RecordExecutionAttempt(state.ExecutionAttempt{ID: id, RunID: "run", ActivationID: req.ActivationID, RetryNumber: 2, Executor: req.Executor, State: "queued", StartedAt: time.Now(), IdempotencyKey: req.IdempotencyKey, Effect: req.Effect}); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueueDispatch(state.Dispatch{AssignmentID: id, AttemptID: id, ActivationID: req.ActivationID, RunID: "run", Executor: req.Executor, Request: raw}); err != nil {
		t.Fatal(err)
	}
}

func TestAnyAdmissionPrefersFreeWorkerOverDiversity(t *testing.T) {
	s := openTemp(t)
	selectionRun(t, s)
	selectionWorker(t, s, "a", 1, "agent/a")
	selectionWorker(t, s, "b", 1, "agent/b")
	selectionDispatch(t, s, "busy", workers.ExecuteRequest{Executor: "agent/a"})
	if d, _ := admissionClaim(t, s, "a"); d == nil {
		t.Fatal("missing busy assignment")
	}
	selectionDispatch(t, s, "any", workers.ExecuteRequest{AgentSelection: &workers.AgentSelectionPreferences{PreviousExecutors: []string{"agent/b"}}})
	d, _ := admissionClaim(t, s, "b")
	if d == nil || d.Executor != "agent/b" {
		t.Fatalf("free worker ignored: %+v", d)
	}
}

func TestAnyAdmissionUsesPollingWorkerForSelectedAgent(t *testing.T) {
	s := openTemp(t)
	selectionRun(t, s)
	selectionWorker(t, s, "a", 1, "agent/a")
	selectionWorker(t, s, "b", 1, "agent/a")
	selectionDispatch(t, s, "any", workers.ExecuteRequest{})
	d, _ := admissionClaim(t, s, "b")
	if d == nil || d.Executor != "agent/a" {
		t.Fatalf("idle polling worker waited for another worker offering the same agent: %+v", d)
	}
}

func TestAnyAdmissionRespectsExecutorCapacity(t *testing.T) {
	s := openTemp(t)
	selectionRun(t, s)
	if err := s.ConfigureAdmission(state.AdmissionConfig{ExecutorCaps: map[string]int{"agent/a": 1}}); err != nil {
		t.Fatal(err)
	}
	selectionWorker(t, s, "busy", 1, "agent/a")
	selectionWorker(t, s, "free", 2, "agent/a", "agent/b")
	selectionDispatch(t, s, "busy", workers.ExecuteRequest{Executor: "agent/a"})
	if d, _ := admissionClaim(t, s, "busy"); d == nil {
		t.Fatal("missing busy assignment")
	}
	selectionDispatch(t, s, "any", workers.ExecuteRequest{})
	d, _ := admissionClaim(t, s, "free")
	if d == nil || d.Executor != "agent/b" {
		t.Fatalf("executor capacity ignored: %+v", d)
	}
}

func TestAnyAdmissionDoesNotWaitForImpossibleAlternative(t *testing.T) {
	s := openTemp(t)
	selectionRun(t, s)
	if err := s.ConfigureAdmission(state.AdmissionConfig{Executors: map[string]state.AdmissionDemand{"agent/b": {Slots: 2}}}); err != nil {
		t.Fatal(err)
	}
	selectionWorker(t, s, "worker", 1, "agent/a", "agent/b")
	selectionDispatch(t, s, "retry", workers.ExecuteRequest{AgentSelection: &workers.AgentSelectionPreferences{FailedExecutors: []string{"agent/a"}}})
	d, _ := admissionClaim(t, s, "worker")
	if d == nil || d.Executor != "agent/a" {
		t.Fatalf("waited for alternative exceeding hard capacity: %+v", d)
	}
}

func TestAnyAdmissionSelectsAndReservesAtomically(t *testing.T) {
	s := openTemp(t)
	selectionRun(t, s)
	selectionWorker(t, s, "worker", 2, "agent/a", "agent/b")
	for i := 0; i < 3; i++ {
		selectionDispatch(t, s, fmt.Sprintf("any-%d", i), workers.ExecuteRequest{})
	}
	var wg sync.WaitGroup
	results := make(chan *state.Dispatch, 2)
	errors := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, _, err := s.ClaimDispatch("worker", time.Now().Add(time.Hour))
			results <- d
			errors <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	names := map[string]bool{}
	for d := range results {
		if d == nil {
			t.Fatal("missing concurrent assignment")
		}
		names[d.Executor] = true
		attempts, err := s.ListExecutionAttempts("run", d.ActivationID)
		if err != nil || len(attempts) != 1 || attempts[0].Executor != d.Executor || attempts[0].State != "running" {
			t.Fatalf("choice not durable: %+v %v", attempts, err)
		}
	}
	if len(names) != 2 {
		t.Fatalf("concurrent selection ignored reservations: %v", names)
	}
	if d, _ := admissionClaim(t, s, "worker"); d != nil {
		t.Fatal("worker capacity exceeded")
	}
}

func TestAnyAdmissionWaitsWithoutPinningBusyAgent(t *testing.T) {
	s := openTemp(t)
	selectionRun(t, s)
	selectionWorker(t, s, "worker", 1, "agent/a", "agent/b")
	selectionDispatch(t, s, "busy", workers.ExecuteRequest{Executor: "agent/a"})
	busy, generation := admissionClaim(t, s, "worker")
	selectionDispatch(t, s, "any", workers.ExecuteRequest{AgentSelection: &workers.AgentSelectionPreferences{PreviousExecutors: []string{"agent/a"}}})
	if d, _ := admissionClaim(t, s, "worker"); d != nil {
		t.Fatal("busy worker admitted another assignment")
	}
	raw, err := s.AssignmentRequest("any")
	if err != nil {
		t.Fatal(err)
	}
	var queued workers.ExecuteRequest
	if err := json.Unmarshal(raw, &queued); err != nil {
		t.Fatal(err)
	}
	if queued.Executor != "any" {
		t.Fatalf("busy choice pinned prematurely: %s", queued.Executor)
	}
	admissionComplete(t, s, "worker", busy, generation)
	d, _ := admissionClaim(t, s, "worker")
	if d == nil || d.Executor != "agent/b" {
		t.Fatalf("free capacity not reconsidered: %+v", d)
	}
}

func TestAnyAdmissionFailedAgentWaitsForFreshAlternative(t *testing.T) {
	s := openTemp(t)
	selectionRun(t, s)
	selectionWorker(t, s, "a", 1, "agent/a")
	selectionWorker(t, s, "b", 1, "agent/b")
	selectionDispatch(t, s, "busy", workers.ExecuteRequest{Executor: "agent/b"})
	busy, generation := admissionClaim(t, s, "b")
	selectionDispatch(t, s, "retry", workers.ExecuteRequest{ActivationID: "step"})
	if err := s.RecordExecutionAttempt(state.ExecutionAttempt{ID: "failed", RunID: "run", ActivationID: "step", RetryNumber: 1, Executor: "agent/a", State: "failed"}); err != nil {
		t.Fatal(err)
	}
	if d, _ := admissionClaim(t, s, "a"); d != nil {
		t.Fatalf("retried failed agent while alternative exists: %+v", d)
	}
	admissionComplete(t, s, "b", busy, generation)
	d, retryGeneration := admissionClaim(t, s, "b")
	if d == nil || d.Executor != "agent/b" {
		t.Fatalf("fresh agent not selected: %+v", d)
	}
	admissionComplete(t, s, "b", d, retryGeneration)
	if err := s.RecordExecutionAttempt(state.ExecutionAttempt{ID: "retry", RunID: "run", ActivationID: "step", RetryNumber: 2, Executor: "agent/b", State: "failed"}); err != nil {
		t.Fatal(err)
	}
	selectionDispatch(t, s, "retry-again", workers.ExecuteRequest{ActivationID: "step"})
	d, _ = admissionClaim(t, s, "a")
	if d == nil || d.Executor != "agent/a" {
		t.Fatalf("all agents tried; repeated most recent failure: %+v", d)
	}
}

func TestAnyAdmissionKeyedRetryRetainsExecutor(t *testing.T) {
	s := openTemp(t)
	selectionRun(t, s)
	selectionWorker(t, s, "worker", 1, "agent/a", "agent/b")
	selectionDispatch(t, s, "retry", workers.ExecuteRequest{ActivationID: "step", IdempotencyKey: "key", Effect: "idempotent-with-key"})
	if err := s.RecordExecutionAttempt(state.ExecutionAttempt{ID: "failed", RunID: "run", ActivationID: "step", RetryNumber: 1, Executor: "agent/b", IdempotencyKey: "key", Effect: "idempotent-with-key", State: "failed"}); err != nil {
		t.Fatal(err)
	}
	d, _ := admissionClaim(t, s, "worker")
	if d == nil || d.Executor != "agent/b" {
		t.Fatalf("keyed retry left deduplication domain: %+v", d)
	}
}

func TestAnyAdmissionRotationSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "selection.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	selectionRun(t, s)
	selectionWorker(t, s, "worker", 1, "agent/a", "agent/b")
	selectionDispatch(t, s, "first", workers.ExecuteRequest{})
	d, generation := admissionClaim(t, s, "worker")
	if d == nil || d.Executor != "agent/a" {
		t.Fatalf("first choice: %+v", d)
	}
	admissionComplete(t, s, "worker", d, generation)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	selectionDispatch(t, s, "second", workers.ExecuteRequest{})
	d, _ = admissionClaim(t, s, "worker")
	if d == nil || d.Executor != "agent/b" {
		t.Fatalf("rotation reset after reopen: %+v", d)
	}
}
