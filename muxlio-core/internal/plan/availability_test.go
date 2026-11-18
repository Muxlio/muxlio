package plan

import (
	"testing"
	"time"

	"github.com/muxlio/muxlio-adapters/contract"
	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestResolveAvailableExecutorDoesNotBypassFallbackRules(t *testing.T) {
	store := state.New()
	engine := &Engine{store: store, executor: workers.NewFake()}
	if err := workers.RecordAvailability(store, "local", "agent/primary", &contract.Availability{
		Reason: contract.AvailabilityQuotaExhausted, Message: "provider quota exhausted",
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	// Fallbacks are selected by executeExecute retry rules, not here.
	_, err := engine.resolveAvailableExecutor("agent/primary", &Node{
		Executor:          "agent/primary",
		FallbackExecutors: []string{"agent/fallback"},
	})
	if err == nil {
		t.Fatal("blocked primary must not auto-switch to fallback during resolve")
	}
}

func TestResolveAvailableExecutorRejectsWithoutAlternative(t *testing.T) {
	store := state.New()
	engine := &Engine{store: store, executor: workers.NewFake()}
	if err := workers.RecordAvailability(store, "local", "agent/primary", &contract.Availability{
		Reason: contract.AvailabilityAuthenticationFailed,
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	_, err := engine.resolveAvailableExecutor("agent/primary", &Node{Executor: "agent/primary"})
	if err == nil {
		t.Fatal("expected unavailable error")
	}
}

func TestResolveAvailableExecutorAllowsSelectedFallback(t *testing.T) {
	store := state.New()
	engine := &Engine{store: store, executor: workers.NewFake()}
	if err := workers.RecordAvailability(store, "local", "agent/primary", &contract.Availability{
		Reason: contract.AvailabilityQuotaExhausted, Message: "provider quota exhausted",
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err := engine.resolveAvailableExecutor("agent/fallback", &Node{
		Executor:          "agent/primary",
		FallbackExecutors: []string{"agent/fallback"},
	})
	if err != nil || got != "agent/fallback" {
		t.Fatalf("already-selected fallback must remain eligible: got %q err %v", got, err)
	}
}
