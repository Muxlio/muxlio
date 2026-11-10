package state

import (
	"encoding/json"
	"math"
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

func TestUsageSummaryAggregatesCreditsByUnit(t *testing.T) {
	first, second, other := 0.1, 0.25, 9.0
	attempts := []ExecutionAttempt{
		{Measurements: &contract.Measurements{Credit: &contract.ProviderCredit{Unit: "credit", Amount: &first}}},
		{Measurements: &contract.Measurements{Credit: &contract.ProviderCredit{Unit: "credit", Amount: &second}}},
		{Measurements: &contract.Measurements{Credit: &contract.ProviderCredit{Unit: "point", Amount: &other}}},
		{},
	}
	s := SummarizeUsage(attempts)
	if len(s.Credits) != 2 || s.Credits[0].Unit != "credit" || s.Credits[0].Observed == nil || *s.Credits[0].Observed != 0.35 || s.Credits[0].Unknown != 2 || s.Credits[1].Unit != "point" || s.Credits[1].Unknown != 3 {
		t.Fatalf("credits=%+v", s.Credits)
	}
}

func TestUsageSummaryPartialTokensRemainUnknown(t *testing.T) {
	partial := contract.NewTokenUsage(ptrInt64(9), nil, nil)
	s := SummarizeUsage([]ExecutionAttempt{{Usage: partial}})
	if s.Tokens.Observed != nil || s.Tokens.Unknown != 1 {
		t.Fatalf("partial usage was treated as complete: %+v", s.Tokens)
	}
}

func TestUsageSummaryObservedCostPrecedesEstimateOnAttempt(t *testing.T) {
	observed, estimated := 0.2, 0.9
	s := SummarizeUsage([]ExecutionAttempt{{Measurements: &contract.Measurements{CostUSD: &observed, EstimatedCostUSD: &estimated}}})
	if s.CostUSD.Observed == nil || *s.CostUSD.Observed != 0.2 || s.CostUSD.Estimated != nil || s.CostUSD.Unknown != 0 {
		t.Fatalf("cost=%+v", s.CostUSD)
	}
}

func TestUsageSummaryInvalidMeasurementsRemainUnknown(t *testing.T) {
	bad := -1.0
	s := SummarizeUsage([]ExecutionAttempt{{Measurements: &contract.Measurements{EstimatedCostUSD: &bad}}})
	if s.CostUSD.Observed != nil || s.CostUSD.Estimated != nil || s.CostUSD.Unknown != 1 {
		t.Fatalf("invalid measurement counted: %+v", s.CostUSD)
	}
}

func TestUsageSummaryCostOverflowCapsAtMaxFloat64(t *testing.T) {
	first := math.MaxFloat64 - 1
	second := 2.0
	s := SummarizeUsage([]ExecutionAttempt{
		{Measurements: &contract.Measurements{EstimatedCostUSD: &first}},
		{Measurements: &contract.Measurements{EstimatedCostUSD: &second}},
	})
	if s.CostUSD.Estimated == nil || *s.CostUSD.Estimated != math.MaxFloat64 {
		t.Fatalf("cost=%+v", s.CostUSD.Estimated)
	}
}

func ptrInt64(v int64) *int64 { return &v }
