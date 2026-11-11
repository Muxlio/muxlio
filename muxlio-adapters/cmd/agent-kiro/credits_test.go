package main

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-adapters/accounting"
	"github.com/muxlio/muxlio-adapters/contract"
)

func TestLiveCreditsRetainTurnIdentityAndExactPrecision(t *testing.T) {
	raw, err := os.ReadFile("testdata/2.37.0-success.json")
	if err != nil {
		t.Fatal(err)
	}
	amount := parseKiroCreditAmount(raw)
	id := kiroCreditTurnID(raw)
	if amount == nil || *amount != "0.06634344693200664" || id == "" {
		t.Fatalf("live usage missing: %v %s", amount, id)
	}
	r := accounting.NewRecorder()
	part := accounting.Part{Scope: id, Entry: contract.AccountingEntry{ID: id, Credits: []contract.CreditMeasure{{Provider: "kiro", Unit: "credit", Amount: contract.Measure[contract.Amount]{Collected: &contract.Reading[contract.Amount]{Value: *amount, Source: "kiro-turn", Fidelity: "metered"}}}}}}
	for i := 0; i < 2; i++ {
		if err := r.Record(part); err != nil {
			t.Fatal(err)
		}
	}
	other := kiroCreditTurnID(json.RawMessage(strings.ReplaceAll(string(raw), "fixture-turn", "second-turn")))
	part.Scope = other
	part.Entry.ID = other
	if err := r.Record(part); err != nil {
		t.Fatal(err)
	}
	a, err := r.Finish()
	if err != nil {
		t.Fatal(err)
	}
	s, err := accounting.SummarizeAccounting([]accounting.AttemptView{{State: "succeeded", Accounting: a}})
	if err != nil || len(s.Credits) != 1 || s.Credits[0].Value.KnownSubtotal == nil || *s.Credits[0].Value.KnownSubtotal != "0.13268689386401328" {
		t.Fatalf("turn replay or equal-valued turn miscounted: %+v %v", s, err)
	}
}

func TestUnidentifiedCreditsUseEventIdentityAcrossReplaysAndTerminalEchoes(t *testing.T) {
	first := json.RawMessage(`{"update":{"_meta":{"kiro":{"kind":"turn_completion","timestamp":"first-turn","promptTurnSummaries":[{"unit":"credit","usage":0.1}]}}}}`)
	reorderedEcho := json.RawMessage(`{"promptTurnSummaries":[{"usage":0.1,"unit":"credit"}],"timestamp":"first-turn","kind":"turn_completion"}`)
	second := json.RawMessage(strings.ReplaceAll(string(first), "first-turn", "second-turn"))
	if kiroCreditTurnID(first) != kiroCreditTurnID(reorderedEcho) {
		t.Fatal("wrapper or property order changed event identity")
	}
	if kiroCreditTurnID(first) == kiroCreditTurnID(second) {
		t.Fatal("distinct equal-cost turns share an identity")
	}
	r := accounting.NewRecorder()
	for _, raw := range []json.RawMessage{first, first, second, reorderedEcho, second} {
		if err := recordKiroCredits(r, raw); err != nil {
			t.Fatal(err)
		}
	}
	a, err := r.Finish()
	if err != nil {
		t.Fatal(err)
	}
	s, err := accounting.SummarizeAccounting([]accounting.AttemptView{{State: "succeeded", Accounting: a}})
	if err != nil || len(s.Credits) != 1 || s.Credits[0].Value.KnownSubtotal == nil || *s.Credits[0].Value.KnownSubtotal != "0.2" {
		t.Fatalf("replays counted or equal-cost turn lost: %+v %v", s, err)
	}
	if s.Credits[0].Value.EffectiveTotal != nil || s.Credits[0].Value.Completeness != "partial" || len(a.Coverage) != 1 || a.Coverage[0].Reason != "turn-identity-unavailable" {
		t.Fatalf("unidentified events claimed complete coverage: %+v", a)
	}
}

func TestNativeTurnIdentityDeduplicatesButRejectsConflictingAmounts(t *testing.T) {
	r := accounting.NewRecorder()
	first := json.RawMessage(`{"kind":"turn_completion","executionId":"execution","turnId":"one","promptTurnSummaries":[{"unit":"credit","usage":0.1}]}`)
	second := json.RawMessage(strings.ReplaceAll(string(first), `"one"`, `"two"`))
	for _, raw := range []json.RawMessage{first, second, first, second} {
		if err := recordKiroCredits(r, raw); err != nil {
			t.Fatal(err)
		}
	}
	conflict := json.RawMessage(strings.ReplaceAll(string(first), "0.1", "0.2"))
	if err := recordKiroCredits(r, conflict); err == nil {
		t.Fatal("same turn accepted a conflicting credit amount")
	}
	a, _ := r.Finish()
	s, err := accounting.SummarizeAccounting([]accounting.AttemptView{{State: "succeeded", Accounting: a}})
	if err != nil || len(s.Credits) != 1 || s.Credits[0].Value.KnownSubtotal == nil || *s.Credits[0].Value.KnownSubtotal != "0.2" {
		t.Fatalf("conflict changed confirmed credits: %+v %v", s, err)
	}
}

func TestParseKiroCreditAmountAcceptsCreditUnitCasing(t *testing.T) {
	raw := `{"update":{"_meta":{"kiro":{"kind":"turn_completion","promptTurnSummaries":[{"unit":"Credit","usage":0.1}]}}}}`
	got := parseKiroCreditAmount(json.RawMessage(raw))
	if got == nil || *got != "0.1" {
		t.Fatalf("credit = %+v", got)
	}
}

func TestParseKiroCreditsFromTurnCompletion(t *testing.T) {
	if got := parseKiroCreditAmount(json.RawMessage(`{"kind":"turn_completion","promptTurnSummaries":[{"unit":"credit","usage":1e-7}]}`)); got == nil || *got != "0.0000001" {
		t.Fatalf("scientific notation lost: %v", got)
	}
	raw := `{"update":{"sessionUpdate":"session_info_update","_meta":{"kiro":{"kind":"turn_completion","promptTurnSummaries":[{"unit":"credit","usage":0.1}]}}}}`
	got := parseKiroCreditAmount(json.RawMessage(raw))
	if got == nil || *got != "0.1" {
		t.Fatalf("credit = %+v", got)
	}
	for _, bad := range []string{
		`{"update":{"_meta":{"kiro":{"kind":"context_usage","breakdown":{"yourPrompts":{"tokens":1000}}}}}}`,
		`{"update":{"_meta":{"kiro":{"kind":"turn_completion","promptTurnSummaries":[{"unit":"token","usage":1}]}}}}`,
		`{"update":{"_meta":{"kiro":{"kind":"turn_completion","promptTurnSummaries":[{"unit":"credit","usage":-1}]}}}}`,
		`{"update":{"_meta":{"kiro":{"kind":"turn_completion","promptTurnSummaries":[{"unit":"credit","usage":1e308},{"unit":"credit","usage":1e308}]}}}}`,
	} {
		if parseKiroCreditAmount(json.RawMessage(bad)) != nil {
			t.Fatalf("unexpected credit from %s", bad)
		}
	}
}

func TestKiroCreditOnlyFixtureStream(t *testing.T) {
	data, err := os.ReadFile("testdata/credit-only.stream.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	var credits *contract.Amount
	want := contract.Amount("0.18065595854063016")
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var ev map[string]json.RawMessage
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		if reported := parseKiroCreditAmount(ev["data"]); reported != nil {
			credits = reported
		}
	}
	if credits == nil || *credits != want {
		t.Fatalf("fixture credits = %+v, want %v", credits, want)
	}
}
