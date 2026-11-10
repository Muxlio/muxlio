package main

import (
	"encoding/json"
	"testing"
)

func TestParseCursorTokenUsageFoldsCacheReadIntoInput(t *testing.T) {
	raw := `{"inputTokens":49,"outputTokens":222,"cacheReadTokens":9728,"cacheWriteTokens":0}`
	got := parseCursorTokenUsage(json.RawMessage(raw))
	if got == nil {
		t.Fatal("expected usage")
	}
	in, inOK := got.ReportedInput()
	if !inOK || in != 9777 {
		t.Fatalf("input = %d reported=%v, want 9777", in, inOK)
	}
	out, outOK := got.ReportedOutput()
	if !outOK || out != 222 {
		t.Fatalf("output = %d, want 222", out)
	}
	cached, cachedOK := got.ReportedCached()
	if !cachedOK || cached != 9728 {
		t.Fatalf("cached = %d, want 9728", cached)
	}
}

func TestParseCursorTokenUsageFoldsCacheWriteIntoInput(t *testing.T) {
	raw := `{"inputTokens":10,"outputTokens":5,"cacheReadTokens":20,"cacheWriteTokens":15}`
	got := parseCursorTokenUsage(json.RawMessage(raw))
	if got == nil {
		t.Fatal("expected usage")
	}
	in, _ := got.ReportedInput()
	if in != 45 {
		t.Fatalf("input = %d, want 45", in)
	}
}

func TestParseCursorTokenUsageDoesNotReportUncachedInputWhenFoldOverflows(t *testing.T) {
	raw := `{"inputTokens":9223372036854775807,"outputTokens":5,"cacheReadTokens":1}`
	got := parseCursorTokenUsage(json.RawMessage(raw))
	if got == nil {
		t.Fatal("expected independently reported usage")
	}
	if _, ok := got.ReportedInput(); ok {
		t.Fatal("input should be unavailable when folding cache tokens overflows")
	}
	if out, ok := got.ReportedOutput(); !ok || out != 5 {
		t.Fatalf("output = %d reported=%v, want 5", out, ok)
	}
	if cached, ok := got.ReportedCached(); !ok || cached != 1 {
		t.Fatalf("cached = %d reported=%v, want 1", cached, ok)
	}
}

func TestMergeTerminalUsagePreservesPriorDimensionsWhenPartial(t *testing.T) {
	prior := parseCursorTokenUsage(json.RawMessage(`{"inputTokens":100,"outputTokens":40,"cacheReadTokens":12}`))
	terminal := parseCursorTokenUsage(json.RawMessage(`{"inputTokens":110}`))
	got := mergeTerminalUsage(prior, terminal)
	if got == nil {
		t.Fatal("expected merged usage")
	}
	in, _ := got.ReportedInput()
	out, outOK := got.ReportedOutput()
	cached, cachedOK := got.ReportedCached()
	if in != 110 || !outOK || out != 40 || !cachedOK || cached != 12 {
		t.Fatalf("merged = input=%d output=%d/%v cached=%d/%v", in, out, outOK, cached, cachedOK)
	}
}
