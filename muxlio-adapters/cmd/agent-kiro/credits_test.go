package main

import (
	"bufio"
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-adapters/contract"
)

func TestParseKiroCreditsFromTurnCompletion(t *testing.T) {
	raw := `{"update":{"sessionUpdate":"session_info_update","_meta":{"kiro":{"kind":"turn_completion","promptTurnSummaries":[{"unit":"credit","usage":0.1}]}}}}`
	got := parseKiroCredits(json.RawMessage(raw))
	if got == nil || got.Amount == nil || math.Abs(*got.Amount-0.1) > 1e-9 || got.Unit != "credit" {
		t.Fatalf("credit = %+v", got)
	}
	for _, bad := range []string{
		`{"update":{"_meta":{"kiro":{"kind":"context_usage","breakdown":{"yourPrompts":{"tokens":1000}}}}}}`,
		`{"update":{"_meta":{"kiro":{"kind":"turn_completion","promptTurnSummaries":[{"unit":"token","usage":1}]}}}}`,
		`{"update":{"_meta":{"kiro":{"kind":"turn_completion","promptTurnSummaries":[{"unit":"credit","usage":-1}]}}}}`,
		`{"update":{"_meta":{"kiro":{"kind":"turn_completion","promptTurnSummaries":[{"unit":"credit","usage":1e308},{"unit":"credit","usage":1e308}]}}}}`,
	} {
		if parseKiroCredits(json.RawMessage(bad)) != nil {
			t.Fatalf("unexpected credit from %s", bad)
		}
	}
}

func TestKiroCreditOnlyFixtureStream(t *testing.T) {
	data, err := os.ReadFile("testdata/credit-only.stream.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	var credits *contract.ProviderCredit
	want := 0.18065595854063016
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
		if reported := parseKiroCredits(ev["data"]); reported != nil {
			merged, err := mergeTerminalCredits(credits, reported)
			if err != nil {
				t.Fatal(err)
			}
			credits = merged
		}
	}
	if credits == nil || credits.Amount == nil || math.Abs(*credits.Amount-want) > 1e-9 {
		t.Fatalf("fixture credits = %+v, want %v", credits, want)
	}
}
