package contract

import (
	"encoding/json"
	"testing"
)

func TestResultExactNumbers(t *testing.T) {
	for _, value := range []string{`9007199254740993`, `1e400`, `7`, `0`, `7.0`, `70e-1`, `"9007199254740993"`, `{"values":[9007199254740993,1e400,7,0,"0"]}`} {
		raw := `{"outputs":{"value":` + value + `},"accounting":{"revision":1,"final":true,"entries":[{"id":"call","tokens":{"total":{"collected":{"value":"0","source":"fixture","fidelity":"metered"}}}}]}}`
		r, err := ParseResult([]byte(raw))
		if err != nil {
			t.Error(err)
			continue
		}
		got, _ := json.Marshal(r.Outputs["value"])
		if string(got) != value {
			t.Errorf("got %s want %s", got, value)
		}
		if r.Accounting == nil || r.Accounting.Entries[0].Tokens.Total.Collected.Value != "0" {
			t.Fatal("typed accounting lost")
		}
		for _, suffix := range []string{` {}`, ` trailing`} {
			if _, err := ParseResult([]byte(raw + suffix)); err == nil {
				t.Error("accepted trailing document")
			}
		}
	}
}

func TestDecodeJSONCompleteDocument(t *testing.T) {
	for _, raw := range []string{`1 2`, `{"n":1e400} {}`, `{"n":1} trailing`, `{"n":01}`, ``} {
		var v any
		if err := DecodeJSON([]byte(raw), &v); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
}
