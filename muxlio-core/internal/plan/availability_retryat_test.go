package plan

import (
	"testing"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestResolveAvailableExecutorAllowsPastRetryAt(t *testing.T) {
	store := state.New()
	now := time.Now()
	retry := now.Add(-time.Minute)
	if err := workers.RecordAvailability(store, "local", "agent/primary", &contract.Availability{
		Reason: contract.AvailabilityQuotaExhausted, Message: "quota", RetryAt: &retry,
	}, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	engine := NewEngineWithStore(&Plan{IRVersion: "1.0", Name: "t", Root: "root", Subgraphs: map[string]Subgraph{
		"root": {Nodes: []Node{{ID: "step", Kind: KindExecute, Executor: "agent/primary"}}},
	}}, workers.NewFake(), store)
	resolved, err := engine.resolveAvailableExecutor("agent/primary", &Node{ID: "step", Kind: KindExecute, Executor: "agent/primary"})
	if err != nil {
		t.Fatal(err)
	}
	if resolved != "agent/primary" {
		t.Fatalf("past RetryAt should remain eligible for probe recovery, got %q", resolved)
	}
}

func TestResolveAvailableExecutorStillBlocksAuth(t *testing.T) {
	store := state.New()
	if err := workers.RecordAvailability(store, "local", "agent/primary", &contract.Availability{
		Reason: contract.AvailabilityAuthenticationFailed, Message: "auth",
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	engine := NewEngineWithStore(&Plan{IRVersion: "1.0", Name: "t", Root: "root", Subgraphs: map[string]Subgraph{
		"root": {Nodes: []Node{{ID: "step", Kind: KindExecute, Executor: "agent/primary"}}},
	}}, workers.NewFake(), store)
	if _, err := engine.resolveAvailableExecutor("agent/primary", &Node{ID: "step", Kind: KindExecute, Executor: "agent/primary"}); err == nil {
		t.Fatal("auth block without RetryAt must still reject admission")
	}
}
