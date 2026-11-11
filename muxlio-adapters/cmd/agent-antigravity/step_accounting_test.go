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
