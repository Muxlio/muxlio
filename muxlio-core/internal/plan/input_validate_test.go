package plan

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateNullableCommandOutputs(t *testing.T) {
	outputs := map[string]OutputDecl{
		"signal":   {Schema: json.RawMessage(`{"anyOf":[{"type":"string"},{"type":"null"}]}`)},
		"exitCode": {Schema: json.RawMessage(`{"anyOf":[{"type":"number"},{"type":"null"}]}`)},
	}
	for _, tc := range []struct {
		name   string
		values map[string]any
		valid  bool
	}{
		{"normal exit", map[string]any{"signal": nil, "exitCode": 0}, true},
		{"signal termination", map[string]any{"signal": "terminated", "exitCode": nil}, true},
		{"invalid signal", map[string]any{"signal": false, "exitCode": 0}, false},
		{"invalid exit code", map[string]any{"signal": nil, "exitCode": "zero"}, false},
		{"missing signal", map[string]any{"exitCode": 0}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateExecuteOutputs(outputs, tc.values); (err == nil) != tc.valid {
				t.Fatalf("validation error = %v, want valid = %v", err, tc.valid)
			}
		})
	}
}

func TestValidateInputEnforcesArrayBoundsAndStringMinLength(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"required": ["features"],
		"properties": {
			"repository": {"type": "string"},
			"branch": {"type": "string"},
			"features": {
				"type": "array",
				"minItems": 1,
				"maxItems": 20,
				"items": {
					"type": "object",
					"required": ["id", "feature"],
					"properties": {
						"id": {"type": "string", "minLength": 1},
						"feature": {"type": "string", "minLength": 1}
					}
				}
			}
		}
	}`)
	p := &Plan{InputSchema: schema}

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "missing features",
			input: `{"repository":"r","branch":"main"}`,
			want:  `missing required field "features"`,
		},
		{
			name:  "empty features",
			input: `{"repository":"r","branch":"main","features":[]}`,
			want:  "at least 1 item",
		},
		{
			name:  "too many features",
			input: `{"repository":"r","branch":"main","features":[` + strings.Repeat(`{"id":"a","feature":"x"},`, 20) + `{"id":"b","feature":"x"}]}`,
			want:  "at most 20 item",
		},
		{
			name:  "empty feature id",
			input: `{"repository":"r","branch":"main","features":[{"id":"","feature":"x"}]}`,
			want:  "at least 1 character",
		},
		{
			name:  "duplicate feature ids",
			input: `{"repository":"r","branch":"main","features":[{"id":"same","feature":"a"},{"id":"same","feature":"b"}]}`,
			want:  "duplicate id",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateInput(p, json.RawMessage(tc.input))
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tc.want)
			}
		})
	}

	ok := `{"repository":"r","branch":"main","features":[{"id":"one","feature":"do thing"}]}`
	if err := ValidateInput(p, json.RawMessage(ok)); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
}
