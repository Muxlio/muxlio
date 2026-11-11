package main

import (
	"encoding/json"
	"github.com/muxlio/muxlio-adapters/adapter"
	"github.com/muxlio/muxlio-adapters/contract"
	"testing"
)

func TestStepAccountingDeduplicatesIdentityAndRetainsDistinctEqualSteps(t *testing.T) {
	r := adapter.NewTokenRecorder("fixture")
	i := 2
	u := stepUsageUpdate{ConversationID: "conversation", StepIndex: &i, State: "ACTIVE", Usage: json.RawMessage(`{"input_tokens":100,"output_tokens":20}`)}
	if err := observeStepUsage(r, u); err != nil {
		t.Fatal(err)
	}
	u.State = "DONE"
	if err := observeStepUsage(r, u); err != nil {
		t.Fatal(err)
	}
	if err := observeStepUsage(r, u); err != nil {
		t.Fatal(err)
	}
	i = 3
	if err := observeStepUsage(r, u); err != nil {
		t.Fatal(err)
	}
	a := r.Finish()
	if a == nil || len(a.Entries) != 1 || a.Entries[0].Tokens.Total.Collected.Value != "240" {
		t.Fatalf("step identities counted incorrectly: %+v", a)
	}
}

func TestAntigravityKeepsUsageWhenOptionalSubsetNull(t *testing.T) {
	for _, raw := range []string{
		`{"input_tokens":100,"output_tokens":20,"thinking_tokens":null,"cache_write_tokens":null,"total_tokens":120}`,
		`{"input_tokens":100,"output_tokens":20,"thinking_tokens":"invalid","cache_write_tokens":-1,"total_tokens":120}`,
	} {
		u := antigravityTokenCounters(json.RawMessage(raw))
		if u == nil || u.Input != 100 || u.Output != 20 || u.Total == nil || *u.Total != 120 {
			t.Fatalf("invalid optional subset fields should not discard core usage for %s: %+v", raw, u)
		}
		if u.Reasoning != nil || u.CacheWrite != nil {
			t.Fatalf("expected unavailable optional fields to stay nil for %s: %+v", raw, u)
		}
	}
}

func TestAntigravityKeepsTotalOnlyAndReasoningSubset(t *testing.T) {
	for _, tc := range []struct{ raw, total, reason string }{{`{"total_tokens":100}`, "100", ""}, {`{"input_tokens":100,"output_tokens":20,"thinking_tokens":5,"total_tokens":120}`, "120", "5"}} {
		r := adapter.NewTokenRecorder("fixture")
		if err := r.Delta("step", antigravityTokenCounters(json.RawMessage(tc.raw))); err != nil {
			t.Fatal(err)
		}
		a := r.Finish()
		if a == nil || len(a.Entries) != 1 || a.Entries[0].Tokens.Total.Collected.Value != contract.Count(tc.total) {
			t.Fatalf("lost total-only report: %+v", a)
		}
		if tc.reason != "" && a.Entries[0].Tokens.Reasoning.Collected.Value != contract.Count(tc.reason) {
			t.Fatal("reasoning subset lost")
		}
	}
}
