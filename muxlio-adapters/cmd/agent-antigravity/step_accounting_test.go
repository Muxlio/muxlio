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
		u := mustAntigravityTokenCounters(t, json.RawMessage(raw))
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
		if err := r.Delta("step", mustAntigravityTokenCounters(t, json.RawMessage(tc.raw))); err != nil {
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

func mustAntigravityTokenCounters(t *testing.T, raw json.RawMessage) *adapter.TokenCounters {
	t.Helper()
	usage, err := antigravityTokenCounters(raw)
	if err != nil {
		t.Fatal(err)
	}
	return usage
}

func TestAntigravityNativeCacheNormalization(t *testing.T) {
	for _, tc := range []struct {
		name, raw    string
		input, total *int64
		cached       int64
	}{
		{"native", `{"input_tokens":2373,"output_tokens":120,"cache_read_tokens":6149,"total_tokens":2493}`, int64Ptr(8522), int64Ptr(8642), 6149},
		{"cache smaller than uncached input", `{"input_tokens":100,"output_tokens":20,"cache_read_tokens":30,"total_tokens":120}`, int64Ptr(130), int64Ptr(150), 30},
		{"reported zeros", `{"input_tokens":0,"output_tokens":0,"cache_read_tokens":0,"total_tokens":0}`, int64Ptr(0), int64Ptr(0), 0},
		{"missing input", `{"cache_read_tokens":30,"total_tokens":50}`, nil, int64Ptr(80), 30},
		{"legacy inclusive", `{"inputTokens":100,"outputTokens":20,"cachedTokens":30}`, int64Ptr(100), nil, 30},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := mustAntigravityTokenCounters(t, json.RawMessage(tc.raw))
			input, ok := u.ReportedInput()
			if ok != (tc.input != nil) || ok && input != *tc.input {
				t.Fatalf("input=%d reported=%v, want %v", input, ok, tc.input)
			}
			if (u.Total == nil) != (tc.total == nil) || u.Total != nil && *u.Total != *tc.total {
				t.Fatalf("total=%v, want %v", u.Total, tc.total)
			}
			if cached, ok := u.ReportedCached(); !ok || cached != tc.cached {
				t.Fatalf("cached=%d reported=%v", cached, ok)
			}
		})
	}
}

func TestAntigravityCacheNormalizationRejectsOverflow(t *testing.T) {
	for _, raw := range []string{
		`{"input_tokens":9223372036854775807,"cache_read_tokens":1}`,
		`{"input_tokens":1,"total_tokens":9223372036854775807,"cache_read_tokens":1}`,
	} {
		if u, err := antigravityTokenCounters(json.RawMessage(raw)); err == nil || u != nil {
			t.Fatalf("overflow accepted: %+v %v", u, err)
		}
	}
}

func TestStepWithoutUsageNeedsNoAccountingIdentity(t *testing.T) {
	r := adapter.NewTokenRecorder("fixture")
	defer r.Finish()
	if err := observeStepUsage(r, stepUsageUpdate{State: "DONE"}); err != nil {
		t.Fatal(err)
	}
}

func int64Ptr(n int64) *int64 { return &n }
