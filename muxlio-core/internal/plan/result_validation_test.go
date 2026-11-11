package plan

import (
	"encoding/json"
	"errors"
	"github.com/muxlio/muxlio-core/internal/testaccounting"
	"strings"
	"testing"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/muxlio/muxlio-core/internal/workers"
)

func TestRuntimeJSONSchema(t *testing.T) {
	for _, tc := range []struct {
		name, schema, value string
		valid               bool
	}{
		{"boolean prose", `{"type":"boolean"}`, `"looks good"`, false},
		{"false is a valid result", `{"type":"boolean"}`, `false`, true},
		{"boolean const", `{"const":true}`, `false`, false},
		{"numeric enum", `{"type":"number","enum":[1,2]}`, `3`, false},
		{"additional properties", `{"type":"object","properties":{"ok":{"type":"boolean"}},"additionalProperties":false}`, `{"ok":true,"extra":1}`, false},
		{"composition", `{"allOf":[{"type":"number"},{"exclusiveMinimum":0}]}`, `0`, false},
		{"oneOf ambiguity", `{"oneOf":[{"type":"number"},{"type":"integer"}]}`, `1`, false},
		{"union type", `{"type":["boolean","null"]}`, `null`, true},
		{"pattern", `{"type":"string","pattern":"^ok$"}`, `"looks good"`, false},
		{"unicode length", `{"type":"string","maxLength":1}`, `"é"`, true},
		{"local reference", `{"$defs":{"approval":{"type":"boolean"}},"$ref":"#/$defs/approval"}`, `"yes"`, false},
		{"unique items", `{"type":"array","uniqueItems":true}`, `[1,1]`, false},
		{"unconstrained", `{}`, `false`, true},
		{"false schema", `false`, `null`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := json.RawMessage(tc.schema)
			var value any
			if err := json.Unmarshal([]byte(tc.value), &value); err != nil {
				t.Fatal(err)
			}
			for _, err := range []error{
				ValidateInput(&Plan{InputSchema: schema}, json.RawMessage(tc.value)),
				ValidateExecuteOutputs(map[string]OutputDecl{"approval": {Schema: schema}}, map[string]any{"approval": value}),
			} {
				if (err == nil) != tc.valid {
					t.Fatalf("valid=%v: %v", tc.valid, err)
				}
				if err != nil {
					var diagnostic *ValueValidationFailure
					if !errors.As(err, &diagnostic) || len(diagnostic.Expected) == 0 || len(diagnostic.Actual) == 0 {
						t.Fatalf("missing typed diagnostic: %v", err)
					}
				}
			}
		})
	}
}

func TestRuntimeSchemaErrors(t *testing.T) {
	for _, schema := range []string{`{"type":"bogus"}`, `{"$ref":"file:///does-not-exist/schema.json"}`, `{"$ref":"https://example.test/schema.json"}`} {
		err := ValidateInput(&Plan{InputSchema: json.RawMessage(schema)}, json.RawMessage(`{}`))
		var diagnostic *ValueValidationFailure
		if !errors.As(err, &diagnostic) || diagnostic.Code != InvalidSchema {
			t.Fatalf("schema %s: %v", schema, err)
		}
	}
	for _, input := range []string{`{"ok":true} {}`, `looks good`} {
		err := ValidateInput(&Plan{InputSchema: json.RawMessage(`{}`)}, json.RawMessage(input))
		var diagnostic *ValueValidationFailure
		if !errors.As(err, &diagnostic) || diagnostic.Code != InputValidationFailed {
			t.Fatalf("input %s: %v", input, err)
		}
	}
}

func TestNamedResultValidationPersistsFailureAndArtifacts(t *testing.T) {
	for _, tc := range []struct {
		name    string
		outputs map[string]any
		missing bool
	}{
		{"prose", map[string]any{"approval": "looks good"}, false},
		{"assistant text cannot fill a named port", map[string]any{"_text": map[string]any{"data": "looks good"}}, true},
		{"null is present but invalid", map[string]any{"approval": nil}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := state.New()
			if err := store.CreateRun(&state.Run{ID: "r"}); err != nil {
				t.Fatal(err)
			}
			if err := store.CreateActivation(&state.Activation{ID: "r:task", RunID: "r", NodeID: "task", State: "running"}); err != nil {
				t.Fatal(err)
			}
			artifact := map[string]any{"digest": "sha256:artifact", "mime": "text/markdown", "path": "notes.md"}
			tc.outputs["notes"] = artifact
			exec := workers.NewFake().ByNode("task", workers.ExecuteResult{Outputs: tc.outputs, Accounting: testaccounting.Tokens(0, 0, 0)})
			engine := NewEngineWithStore(&Plan{}, exec, store)
			// Native structured generation is a dispatch requirement, never a reason to
			// bypass consumer validation of what was actually returned.
			node := &Node{ID: "task", Executor: "test", RequiredCapabilities: map[string]string{"nativeStructuredOutput": "1"},
				Outputs: map[string]OutputDecl{
					"approval": {Schema: json.RawMessage(`{"type":"boolean"}`)},
					"notes":    {Path: "notes.md", Mime: "text/markdown", Schema: json.RawMessage(`{"type":"boolean"}`)},
				}, Retry: &RetryPolicy{MaxAttempts: 2}}
			_, err := engine.executeExecute("r", node, nil, &EvalContext{})
			var diagnostic *ValueValidationFailure
			if !errors.As(err, &diagnostic) || diagnostic.Code != OutputValidationFailed || diagnostic.Missing != tc.missing {
				t.Fatalf("validation error: %v", err)
			}
			attempts, err := store.ListExecutionAttempts("r", "r:task")
			if err != nil || len(attempts) != 2 {
				t.Fatalf("attempts=%+v err=%v", attempts, err)
			}
			for _, attempt := range attempts {
				if attempt.State != "failed" || attempt.Result == nil || !attempt.Result.ExecutionSucceeded || attempt.Result.ValidationFailure.Code != OutputValidationFailed || !strings.Contains(attempt.Failure, "expected") || !strings.Contains(attempt.Failure, "actual") {
					t.Fatalf("attempt=%+v", attempt)
				}
				if attempt.Result.Outputs["notes"].(map[string]any)["mime"] != "text/markdown" {
					t.Fatalf("lost inspectable artifact: %+v", attempt.Result)
				}
				if attempt.Accounting == nil || attempt.ExecutionDurationMS == nil {
					t.Fatal("lost execution measurements")
				}
			}
			events, err := store.GetEvents("r", 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range events {
				if event.Type == state.EventExecutionAttempt && event.Payload["state"] == "succeeded" {
					t.Fatal("invalid result recorded as successful")
				}
			}
		})
	}
}

func TestMissingPortWithEmptySchema(t *testing.T) {
	err := ValidateExecuteOutputs(map[string]OutputDecl{"approval": {Schema: json.RawMessage(`{}`)}}, nil)
	var diagnostic *ValueValidationFailure
	if !errors.As(err, &diagnostic) || !diagnostic.Missing {
		t.Fatalf("missing port accepted: %v", err)
	}
}

func TestResultDecodingFailureCannotSatisfyStringSchema(t *testing.T) {
	outputs := map[string]OutputDecl{"message": {Schema: json.RawMessage(`{"type":"string"}`)}}
	result := workers.ExecuteResult{Outputs: map[string]any{"message": "looks good"}, OutputErrors: map[string]string{"message": "result port must contain JSON"}}
	err := ValidateExecuteResult(outputs, result)
	var diagnostic *ValueValidationFailure
	if !errors.As(err, &diagnostic) || diagnostic.Code != OutputValidationFailed {
		t.Fatalf("prose accepted as JSON: %v", err)
	}
	result.OutputErrors = nil
	if err := ValidateExecuteResult(outputs, result); err != nil {
		t.Fatalf("JSON string rejected: %v", err)
	}
}

func TestRuntimeSchemaPreservesNumericConstraints(t *testing.T) {
	schema := json.RawMessage(`{"const":9007199254740993}`)
	for _, tc := range []struct {
		value string
		valid bool
	}{
		{"9007199254740993", true}, {"9007199254740992", false},
	} {
		for _, err := range []error{
			ValidateInput(&Plan{InputSchema: schema}, json.RawMessage(tc.value)),
			ValidateExecuteOutputs(map[string]OutputDecl{"id": {Schema: schema}}, map[string]any{"id": json.Number(tc.value)}),
		} {
			if (err == nil) != tc.valid {
				t.Fatalf("value=%s valid=%v: %v", tc.value, tc.valid, err)
			}
		}
	}
	if err := ValidateInput(&Plan{InputSchema: json.RawMessage(`{} {}`)}, json.RawMessage(`null`)); err == nil {
		t.Fatal("trailing schema JSON accepted")
	}
}
