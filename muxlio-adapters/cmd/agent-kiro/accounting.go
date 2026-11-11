package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/muxlio/muxlio-adapters/accounting"
	"github.com/muxlio/muxlio-adapters/contract"
)

func kiroTurnCompletion(raw json.RawMessage) json.RawMessage {
	var v map[string]json.RawMessage
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	for _, key := range []string{"update", "_meta", "kiro"} {
		if nested, ok := v[key]; ok {
			if completion := kiroTurnCompletion(nested); completion != nil {
				return completion
			}
		}
	}
	var kind string
	_ = json.Unmarshal(v["kind"], &kind)
	if kind != "turn_completion" {
		return nil
	}
	return raw
}

func kiroCreditTurnID(raw json.RawMessage) string {
	completion := kiroTurnCompletion(raw)
	if completion == nil {
		return ""
	}
	var v map[string]json.RawMessage
	if json.Unmarshal(completion, &v) != nil {
		return ""
	}
	var turn, execution string
	_ = json.Unmarshal(v["turnId"], &turn)
	_ = json.Unmarshal(v["executionId"], &execution)
	if turn != "" {
		return fmt.Sprintf("credit-turn-%x", sha256.Sum256([]byte(execution+"\x00"+turn)))
	}
	// Older/unidentified reports have no proven turn identity. Canonicalize
	// the full turn metadata (not its amount or outer event type) so replayed
	// updates and terminal echoes share an identity. Distinct timestamps,
	// request IDs and other turn metadata retain equal-cost genuine turns.
	var value any
	if contract.DecodeJSON(completion, &value) != nil {
		return ""
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("credit-event-%x", sha256.Sum256(canonical))
}

// Kiro turn metadata reports consumed credits. Context occupancy and tool
// context breakdowns deliberately do not enter this decoder.
func parseKiroCreditAmount(raw json.RawMessage) *contract.Amount {
	raw = kiroTurnCompletion(raw)
	if raw == nil {
		return nil
	}
	var value map[string]json.RawMessage
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	var entries []struct {
		Unit  string      `json:"unit"`
		Usage json.Number `json:"usage"`
	}
	d := json.NewDecoder(bytes.NewReader(value["promptTurnSummaries"]))
	d.UseNumber()
	if d.Decode(&entries) != nil {
		return nil
	}
	var total *contract.Amount
	for _, entry := range entries {
		if strings.ToLower(strings.TrimSpace(entry.Unit)) != "credit" {
			continue
		}
		amount, err := accounting.ParseJSONAmount(json.RawMessage(entry.Usage.String()))
		if err != nil {
			return nil
		}
		if total != nil {
			amount, err = accounting.AddAmount(*total, amount)
			if err != nil {
				return nil
			}
		}
		total = &amount
	}
	return total
}

func recordKiroCredits(recorder *accounting.Recorder, raw json.RawMessage) error {
	amount := parseKiroCreditAmount(raw)
	if amount == nil {
		return nil
	}
	id := kiroCreditTurnID(raw)
	if id == "" {
		return fmt.Errorf("kiro credit event identity unavailable")
	}
	if err := recorder.Record(accounting.Part{Scope: id, Entry: contract.AccountingEntry{ID: id, Credits: []contract.CreditMeasure{{Provider: "kiro", Unit: "credit", Amount: contract.Measure[contract.Amount]{Collected: &contract.Reading[contract.Amount]{Value: *amount, Source: "kiro-turn", Fidelity: "metered"}}}}}}); err != nil {
		return err
	}
	reason := "source-coverage-unverified"
	if strings.HasPrefix(id, "credit-event-") {
		reason = "turn-identity-unavailable"
	}
	return recorder.SetCoverage(contract.AccountingKey{Kind: "credits", Provider: "kiro", Unit: "credit"}, "partial", reason)
}
