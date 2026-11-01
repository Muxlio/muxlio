package plan

import (
	"encoding/json"
	"fmt"
)

// ValidateExecuteOutputs checks structured JSON ports against declared schemas.
func ValidateExecuteOutputs(outputs map[string]OutputDecl, values map[string]any) error {
	for port, decl := range outputs {
		if len(decl.Schema) == 0 || decl.Path != "" {
			continue
		}
		val, ok := values[port]
		if !ok {
			return fmt.Errorf("muxlio: execute output missing port %q", port)
		}
		var schema map[string]any
		if err := json.Unmarshal(decl.Schema, &schema); err != nil {
			return fmt.Errorf("muxlio: invalid output schema on port %q", port)
		}
		if err := validateInputAgainstSchema(val, schema, port); err != nil {
			return err
		}
	}
	return nil
}
