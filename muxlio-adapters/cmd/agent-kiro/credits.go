package main

import (
	"encoding/json"
	"math"
	"strings"

	"github.com/muxlio/muxlio-adapters/contract"
)

// parseKiroCredits extracts provider-reported credits from Kiro turn completion
// metadata. promptTurnSummaries entries use unit "credit"; context-window data
// and token containers never become credits here.
func parseKiroCredits(result json.RawMessage) *contract.ProviderCredit {
	var payload map[string]json.RawMessage
	if json.Unmarshal(result, &payload) != nil {
		return nil
	}
	if raw, ok := payload["update"]; ok {
		if credit := parseKiroCredits(raw); credit != nil {
			return credit
		}
	}
	for _, key := range []string{"_meta", "kiro"} {
		if credit := parseKiroCredits(payload[key]); credit != nil {
			return credit
		}
	}
	summaries, ok := payload["promptTurnSummaries"]
	if !ok {
		return nil
	}
	var kind string
	if json.Unmarshal(payload["kind"], &kind) != nil || kind != "turn_completion" {
		return nil
	}
	var entries []struct {
		Unit  string  `json:"unit"`
		Usage float64 `json:"usage"`
	}
	if json.Unmarshal(summaries, &entries) != nil {
		return nil
	}
	var total *float64
	unit := ""
	for _, entry := range entries {
		if strings.ToLower(strings.TrimSpace(entry.Unit)) != "credit" {
			continue
		}
		if unit == "" {
			unit = "credit"
		}
		usage := entry.Usage
		if math.IsNaN(usage) || math.IsInf(usage, 0) || usage < 0 {
			return nil
		}
		if total == nil {
			total = &usage
			continue
		}
		sum := *total + usage
		if math.IsInf(sum, 0) {
			return nil
		}
		total = &sum
	}
	if unit == "" || total == nil {
		return nil
	}
	return &contract.ProviderCredit{Unit: unit, Amount: total, Provenance: contract.CreditProvenancePerTurn}
}
