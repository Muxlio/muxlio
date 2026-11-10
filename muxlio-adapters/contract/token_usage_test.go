package contract

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTokenUsagePartialFieldsRoundTrip(t *testing.T) {
	raw := `{"input":9,"output":null,"cached":null}`
	var u TokenUsage
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	if v, ok := u.ReportedInput(); !ok || v != 9 {
		t.Fatalf("input=%+v", u)
	}
	if _, outOK := u.ReportedOutput(); outOK {
		t.Fatalf("unexpected output: %+v", u)
	}
	if _, cachedOK := u.ReportedCached(); cachedOK {
		t.Fatalf("unexpected reported fields: %+v", u)
	}
	encoded, err := json.Marshal(&u)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"output":null`) || !strings.Contains(string(encoded), `"cached":null`) {
		t.Fatalf("partial marshal=%s", encoded)
	}
}

func TestAddTokenUsageOverflow(t *testing.T) {
	max := int64(^uint64(0) >> 1)
	_, err := AddTokenUsage(
		NewTokenUsage(&max, int64Ptr(1), int64Ptr(0)),
		NewTokenUsage(int64Ptr(1), int64Ptr(0), int64Ptr(0)),
	)
	if err == nil {
		t.Fatal("expected overflow")
	}
}

func TestAddTokenUsagePreservesReportedDimensions(t *testing.T) {
	first := NewTokenUsage(int64Ptr(9), nil, nil)
	second := NewTokenUsage(nil, int64Ptr(2), nil)
	got, err := AddTokenUsage(first, second)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := got.ReportedInput(); !ok || v != 9 {
		t.Fatalf("input = %d, %t", v, ok)
	}
	if v, ok := got.ReportedOutput(); !ok || v != 2 {
		t.Fatalf("output = %d, %t", v, ok)
	}
	if _, ok := got.ReportedCached(); ok {
		t.Fatal("cached unexpectedly reported")
	}
}

func TestTokenUsageAllDimensionsUnavailableRoundTrip(t *testing.T) {
	usage := UnavailableTokenUsage()
	raw, err := json.Marshal(usage)
	if err != nil {
		t.Fatal(err)
	}
	var decoded TokenUsage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.AnyReported() {
		t.Fatalf("all-null usage reported fields: %s", raw)
	}
}

func int64Ptr(v int64) *int64 { return &v }
