package main

import (
	"encoding/json"
	"testing"
)

func TestParseClaudeTokenUsageFoldsCacheCreationAndRead(t *testing.T) {
	raw := json.RawMessage(`{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":30,"cache_creation_input_tokens":40}`)
	got := parseClaudeTokenUsage(raw)
	if got == nil {
		t.Fatal("expected usage")
	}
	if got.Input != 170 || got.Output != 20 || got.Cached != 30 {
		t.Fatalf("usage = %+v, want Input=170 Output=20 Cached=30", got)
	}
}

func TestParseClaudeTokenUsageFoldsCacheReadOnly(t *testing.T) {
	raw := json.RawMessage(`{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":30}`)
	got := parseClaudeTokenUsage(raw)
	if got == nil {
		t.Fatal("expected usage")
	}
	if got.Input != 130 || got.Output != 50 || got.Cached != 30 {
		t.Fatalf("usage = %+v", got)
	}
}

func TestParseClaudeTokenUsageWithoutCacheExtrasUnchanged(t *testing.T) {
	raw := json.RawMessage(`{"input_tokens":100,"output_tokens":50}`)
	got := parseClaudeTokenUsage(raw)
	if got == nil || got.Input != 100 || got.Output != 50 {
		t.Fatalf("usage = %+v", got)
	}
	if _, ok := got.ReportedCached(); ok {
		t.Fatalf("cached unexpectedly reported: %+v", got)
	}
}

func TestParseClaudeTokenUsagePartialCacheCreate(t *testing.T) {
	raw := json.RawMessage(`{"input_tokens":100,"output_tokens":20,"cache_creation_input_tokens":40}`)
	got := parseClaudeTokenUsage(raw)
	if got == nil {
		t.Fatal("expected usage")
	}
	if got.Input != 140 || got.Output != 20 {
		t.Fatalf("usage = %+v", got)
	}
	if _, ok := got.ReportedCached(); ok {
		t.Fatalf("cached should be unavailable: %+v", got)
	}
}

func TestParseClaudeTokenUsageRejectsInvalidCacheCreation(t *testing.T) {
	for _, raw := range []string{
		`{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":30,"cache_creation_input_tokens":-1}`,
		`{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":30,"cache_creation_input_tokens":"40"}`,
	} {
		if got := parseClaudeTokenUsage(json.RawMessage(raw)); got != nil {
			t.Fatalf("usage for %s = %+v, want unavailable", raw, got)
		}
	}
}
