package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/muxlio/muxlio-adapters/adapter"
	"github.com/muxlio/muxlio-adapters/contract"
)

func TestACPModelSelectionSurvivesUnavailableCost(t *testing.T) {
	a := newACPAccounting("backend")
	a.observe(json.RawMessage(`{"sessionUpdate":"config_option_update","configOptions":[{"category":"model","currentValue":"model-1"}]}`))
	s := a.finish()
	if s == nil || !s.Final || len(s.Models) != 1 || s.Models[0] != (contract.AccountingModel{ID: "model-1", Role: "selected", Source: "acp-session-config"}) || len(s.Entries) != 0 {
		t.Fatalf("model selection lost or consumption invented: %+v", s)
	}
}

func TestACPScientificNotationPreservesExactSessionDelta(t *testing.T) {
	a := newACPAccounting("backend")
	a.session = "session"
	a.observe(json.RawMessage(`{"sessionUpdate":"usage_update","cost":{"amount":0,"currency":"USD"}}`))
	a.started = true
	a.observe(json.RawMessage(`{"sessionUpdate":"usage_update","cost":{"amount":1e-7,"currency":"USD"}}`))
	s := a.finish()
	if s == nil || len(s.Entries) != 1 || len(s.Entries[0].Costs) != 1 || s.Entries[0].Costs[0].Amount.Collected.Value != "0.0000001" {
		t.Fatalf("small monetary delta lost: %+v", s)
	}
}

func TestACPPrePromptBaselineAttributesOnlySessionDelta(t *testing.T) {
	for _, unit := range []string{"credits", "USD"} {
		t.Run(unit, func(t *testing.T) {
			dir := t.TempDir()
			program := filepath.Join(dir, "agent")
			update := func(amount string) string {
				return fmt.Sprintf(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"fresh-session","update":{"sessionUpdate":"usage_update","used":53000,"size":200000,"cost":{"amount":%s,"currency":%q}}}}`, amount, unit)
			}
			script := fmt.Sprintf(`#!/bin/sh
IFS= read -r request
printf '%%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentCapabilities":{}}}'
IFS= read -r request
printf '%%s\n' '%s'
printf '%%s\n' '{"jsonrpc":"2.0","id":2,"result":{"sessionId":"fresh-session"}}'
IFS= read -r request
printf '%%s\n' '%s'
printf '%%s\n' '%s'
printf '%%s\n' '{"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}}'
`, update("4.2"), update("4.6"), update("4.6"))
			if err := os.WriteFile(program, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(params{Program: program, MeteringAuthority: "fixture-provider"})
			result := execute(&adapter.Request{Workspace: dir, WorkDir: t.TempDir(), HostExecutionAllowed: true, Params: raw, Task: "reply OK"})
			if result.Error != "" || result.Accounting == nil {
				t.Fatalf("missing baseline accounting: %+v", result)
			}
			entry := result.Accounting.Entries[0]
			if entry.Tokens != nil {
				t.Fatal("context occupancy became consumption")
			}
			if unit == "credits" {
				if len(entry.Credits) != 1 || entry.Credits[0].Amount.Collected.Value != "0.4" || entry.Credits[0].Unit != "credits" {
					t.Fatalf("incorrect credit delta: %+v", entry)
				}
			} else if len(entry.Costs) != 1 || entry.Costs[0].Amount.Collected.Value != "0.4" || entry.Costs[0].Basis != "unspecified" {
				t.Fatalf("incorrect cost delta: %+v", entry)
			}
		})
	}
}

func TestACPUnownedCostAndResetsNeverBecomeFreeConsumption(t *testing.T) {
	update := func(value string) json.RawMessage {
		return json.RawMessage(`{"sessionUpdate":"usage_update","used":53000,"size":200000,"cost":{"amount":` + value + `,"currency":"credits"}}`)
	}
	for _, baseline := range []bool{false, true} {
		t.Run(fmt.Sprint(baseline), func(t *testing.T) {
			a := newACPAccounting("fixture-provider")
			a.session = "fresh-session"
			if baseline {
				a.observe(update("4.2"))
			}
			a.started = true
			a.observe(update("4.6"))
			a.observe(update("1"))
			a.observe(update("4.7"))
			s := a.finish()
			if !baseline {
				if s != nil {
					t.Fatal("cost without baseline attributed")
				}
				return
			}
			if s == nil || s.Entries[0].Credits[0].Amount.Collected.Value != "0.4" {
				t.Fatal("reset discarded confirmed consumption or established a new baseline")
			}
		})
	}
}
