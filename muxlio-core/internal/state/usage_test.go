package state

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-adapters/contract"
)

func TestUsageSummaryKeepsObservedEstimatedAndUnknownSeparate(t *testing.T) {
	estimatedTokens := int64(7)
	observedCost := 0.25
	estimatedCost := 0.5
	attempts := []ExecutionAttempt{
		{Usage: &TokenUsage{Input: 10, Output: 2, Cached: 5}, Measurements: &contract.Measurements{CostUSD: &observedCost}},
		{Measurements: &contract.Measurements{EstimatedTokens: &estimatedTokens, EstimatedCostUSD: &estimatedCost}},
		{},
	}
	s := SummarizeUsage(attempts)
	if s.Tokens.Observed == nil || *s.Tokens.Observed != 12 || s.Tokens.Estimated == nil || *s.Tokens.Estimated != 7 || s.Tokens.Unknown != 1 {
		t.Fatalf("tokens=%+v", s.Tokens)
	}
	if s.CostUSD.Observed == nil || *s.CostUSD.Observed != 0.25 || s.CostUSD.Estimated == nil || *s.CostUSD.Estimated != 0.5 || s.CostUSD.Unknown != 1 {
		t.Fatalf("cost=%+v", s.CostUSD)
	}
}

func TestUsageSummaryMissingIsNullAndReportedZeroIsZero(t *testing.T) {
	s := SummarizeUsage([]ExecutionAttempt{{}})
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"observed":null`) || s.Tokens.Unknown != 1 {
		t.Fatalf("summary=%s", raw)
	}
	s = SummarizeUsage([]ExecutionAttempt{{Usage: &TokenUsage{}}})
	if s.Tokens.Observed == nil || *s.Tokens.Observed != 0 || s.Tokens.Unknown != 0 || s.CostUSD.Observed != nil || s.CostUSD.Unknown != 1 {
		t.Fatalf("summary=%+v", s)
	}
}

func TestUsageSummaryPartialTokensRemainUnknown(t *testing.T) {
	partial := contract.NewTokenUsage(ptrInt64(9), nil, nil)
	s := SummarizeUsage([]ExecutionAttempt{{Usage: partial}})
	if s.Tokens.Observed != nil || s.Tokens.Unknown != 1 {
		t.Fatalf("partial usage was treated as complete: %+v", s.Tokens)
	}
}

func ptrInt64(v int64) *int64 { return &v }
