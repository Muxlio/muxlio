package plan

import (
	"encoding/json"
	"sort"

	"github.com/muxlio/muxlio-core/internal/workers"
)

// ValidateExecuteOutputs validates every named JSON port, independently of the
// executor's completion status or native structured-output capabilities. Files
// remain artifacts; their bytes and metadata are not JSON result values.
func ValidateExecuteOutputs(outputs map[string]OutputDecl, values map[string]any) error {
	ports := make([]string, 0, len(outputs))
	for port := range outputs {
		ports = append(ports, port)
	}
	sort.Strings(ports)
	for _, port := range ports {
		decl := outputs[port]
		if decl.Path != "" || len(decl.Schema) == 0 {
			continue
		}
		val, ok := values[port]
		if !ok {
			return &ValueValidationFailure{Code: OutputValidationFailed, Path: port, Expected: decl.Schema, Missing: true, Message: "execute output missing named port"}
		}
		if err := validateJSONValue(val, decl.Schema, port, OutputValidationFailed); err != nil {
			return err
		}
	}
	return nil
}

// ValidateExecuteResult also rejects decoding failures reported by adapters:
// plain prose must not become a valid JSON string through a fallback conversion.
func ValidateExecuteResult(outputs map[string]OutputDecl, result workers.ExecuteResult) error {
	ports := make([]string, 0, len(result.OutputErrors))
	for port := range result.OutputErrors {
		ports = append(ports, port)
	}
	sort.Strings(ports)
	for _, port := range ports {
		decl, declared := outputs[port]
		if !declared || decl.Path != "" {
			continue
		}
		actual, present := result.Outputs[port]
		raw, _ := json.Marshal(actual)
		expected := decl.Schema
		if len(expected) == 0 {
			expected = json.RawMessage(`{}`)
		}
		return &ValueValidationFailure{Code: OutputValidationFailed, Path: port, Expected: expected, Actual: raw, Missing: !present, Message: result.OutputErrors[port]}
	}
	return ValidateExecuteOutputs(outputs, result.Outputs)
}
