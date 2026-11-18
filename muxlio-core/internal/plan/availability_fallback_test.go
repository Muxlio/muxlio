package plan

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/storage/sqlite"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestExecuteFallsBackWhenPrimaryAlreadyUnavailable(t *testing.T) {
	store := state.New()
	if err := workers.RecordAvailability(store, "local", "agent/primary", &contract.Availability{
		Reason: contract.AvailabilityQuotaExhausted, Message: "quota",
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	fake := workers.NewFake().ByExecutor("agent/fallback", workers.ExecuteResult{
		Outputs: map[string]any{"result": "ok"},
	})
	engine := NewEngineWithStore(&Plan{IRVersion: "1.0", Name: "t", Root: "root", Subgraphs: map[string]Subgraph{
		"root": {Nodes: []Node{{ID: "step", Kind: KindExecute, Executor: "agent/primary"}}},
	}}, fake, store)
	node := &Node{
		ID: "step", Kind: KindExecute, Executor: "agent/primary",
		FallbackExecutors: []string{"agent/fallback"},
		Retry:             &RetryPolicy{MaxAttempts: 2, Effect: "retryable"},
	}
	if _, err := engine.executeExecute("run", node, nil, &EvalContext{}); err != nil {
		t.Fatal(err)
	}
	attempts, err := store.ListExecutionAttempts("run", "run:step")
	if err != nil {
		t.Fatal(err)
	}
	var sawPrimaryFail, sawFallbackOK bool
	for _, a := range attempts {
		if a.Executor == "agent/primary" && a.State == "failed" && strings.Contains(a.Failure, "executor unavailable") {
			sawPrimaryFail = true
		}
		if a.Executor == "agent/fallback" && a.State == "succeeded" {
			sawFallbackOK = true
		}
	}
	if !sawPrimaryFail || !sawFallbackOK {
		t.Fatalf("expected primary unavailable failure then fallback success: %+v", attempts)
	}
}

func TestUnavailableAttemptsRespectRunBudget(t *testing.T) {
	store := state.New()
	if err := workers.RecordAvailability(store, "local", "agent/primary", &contract.Availability{
		Reason: contract.AvailabilityQuotaExhausted,
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	limit := 1
	fake := workers.NewFake()
	engine := NewEngineWithStore(&Plan{Limits: &RunLimits{MaxAttempts: &limit}}, fake, store)
	node := &Node{
		ID: "step", Kind: KindExecute, Executor: "agent/primary",
		Retry: &RetryPolicy{MaxAttempts: 3, Effect: "retryable"},
	}
	_, err := engine.executeExecute("run", node, nil, &EvalContext{})
	if err == nil || !strings.Contains(err.Error(), "maxAttempts") {
		t.Fatalf("expected attempt budget error, got %v", err)
	}
	attempts, err := store.ListExecutionAttempts("run", "run:step")
	if err != nil || len(attempts) != limit || len(fake.Calls()) != 0 {
		t.Fatalf("attempts=%+v launches=%d err=%v", attempts, len(fake.Calls()), err)
	}
}

func TestResolveAvailableExecutorAllowsOtherWorkerWhenOneBlocked(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now()
	if err := store.RegisterWorker(state.Worker{
		ID: "w1", Capacity: 1, LastSeen: now,
		Capabilities: []state.WorkerCapability{{Name: "agent/primary"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterWorker(state.Worker{
		ID: "w2", Capacity: 1, LastSeen: now,
		Capabilities: []state.WorkerCapability{{Name: "agent/primary"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := workers.RecordAvailability(store, "w1", "agent/primary", &contract.Availability{
		Reason: contract.AvailabilityQuotaExhausted, Message: "quota",
	}, now); err != nil {
		t.Fatal(err)
	}
	engine := NewEngineWithStore(&Plan{IRVersion: "1.0", Name: "t", Root: "root", Subgraphs: map[string]Subgraph{
		"root": {Nodes: []Node{{ID: "step", Kind: KindExecute, Executor: "agent/primary"}}},
	}}, workers.NewFake(), store)
	got, err := engine.resolveAvailableExecutor("agent/primary", &Node{Executor: "agent/primary"})
	if err != nil || got != "agent/primary" {
		t.Fatalf("eligible worker must keep prepare open: got %q err %v", got, err)
	}
}

func TestResolveAvailableExecutorRejectsWhenAllWorkersBlocked(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now()
	if err := store.RegisterWorker(state.Worker{
		ID: "w1", Capacity: 1, LastSeen: now,
		Capabilities: []state.WorkerCapability{{Name: "agent/primary"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := workers.RecordAvailability(store, "w1", "agent/primary", &contract.Availability{
		Reason: contract.AvailabilityAuthenticationFailed, Message: "auth",
	}, now); err != nil {
		t.Fatal(err)
	}
	engine := NewEngineWithStore(&Plan{IRVersion: "1.0", Name: "t", Root: "root", Subgraphs: map[string]Subgraph{
		"root": {Nodes: []Node{{ID: "step", Kind: KindExecute, Executor: "agent/primary"}}},
	}}, workers.NewFake(), store)
	if _, err := engine.resolveAvailableExecutor("agent/primary", &Node{Executor: "agent/primary"}); err == nil {
		t.Fatal("all capable workers blocked must reject prepare")
	}
}
