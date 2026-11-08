package state

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-core/internal/diagnostic"
)

func TestAttemptDiagnosticsNumericBoundsAndRedaction(t *testing.T) {
	for _, actual := range []any{json.Number("9007199254740993"), json.Number("1e400"), json.Number("7"), json.Number("0"), map[string]any{"values": []any{json.Number("9007199254740993"), json.Number("1e400"), json.Number("0")}}} {
		d := diagnostic.New("TEST", "runtime", "password=private-value", "")
		d.Actual = actual
		d.Requirements = map[string]any{"api_key": "private-value"}
		attempts := []ExecutionAttempt{{Result: &AttemptResult{Diagnostic: &d}}}
		got := AttemptDiagnostics(attempts)
		if len(got) != 1 || got[0].ID != d.ID {
			t.Fatalf("diagnostics=%+v", got)
		}
		want, _ := json.Marshal(actual)
		raw, err := json.Marshal(got[0].Actual)
		if err != nil || string(raw) != string(want) {
			t.Fatalf("actual=%s want %s err=%v", raw, want, err)
		}
		encoded, err := json.Marshal(got)
		if err != nil || strings.Contains(string(encoded), "private-value") {
			t.Fatalf("redaction failed: %s err=%v", encoded, err)
		}
	}
	for _, message := range []string{"small", strings.Repeat("x", diagnostic.MaxJSON)} {
		attempts := make([]ExecutionAttempt, diagnostic.MaxIssues+10)
		for i := range attempts {
			d := diagnostic.New("TEST", "runtime", message, "")
			d.Actual = json.Number("1e400")
			attempts[i].Result = &AttemptResult{Diagnostic: &d}
		}
		got := AttemptDiagnostics(attempts)
		raw, err := json.Marshal(got)
		if err != nil || len(got) == 0 || len(got) > diagnostic.MaxIssues || len(raw) > diagnostic.MaxJSON {
			t.Fatalf("bounds: count=%d bytes=%d err=%v", len(got), len(raw), err)
		}
	}
}
