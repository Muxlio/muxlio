package accounting

import (
	"encoding/json"
	"testing"
)

func TestSourceJSONAmountsNormalizeExactlyWithoutRounding(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"1e-7", "0.0000001"}, {"1.8065595854063016e-1", "0.18065595854063016"},
		{"1E+3", "1000"}, {"10e-19", "0.000000000000000001"}, {"0.000", "0"}, {" 1.2500 ", "1.25"},
	} {
		got, err := ParseJSONAmount(json.RawMessage(tc.raw))
		if err != nil || string(got) != tc.want {
			t.Fatalf("%s -> %q %v", tc.raw, got, err)
		}
	}
	for _, raw := range []string{`"1e-7"`, "null", "true", "-0.1", "NaN", "1/2", "01", "1e-19", "1e1000", "1e39", ".5", "1 2"} {
		if got, err := ParseJSONAmount(json.RawMessage(raw)); err == nil {
			t.Fatalf("invalid or unrepresentable amount accepted: %s -> %q", raw, got)
		}
	}
}
