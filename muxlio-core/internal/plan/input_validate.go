package plan

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/muxlio/muxlio-core/internal/state"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	InputValidationFailed  = "MUXLIO_INPUT_VALIDATION_FAILED"
	OutputValidationFailed = "MUXLIO_OUTPUT_VALIDATION_FAILED"
	InvalidSchema          = "MUXLIO_INVALID_SCHEMA"
)

// ValueValidationFailure carries the declared contract and the rejected JSON value.
type ValueValidationFailure = state.ValueValidationFailure

// Runtime schemas are self-contained. References within the schema work, but
// validation must never read arbitrary files or fetch remote resources.
type runtimeSchemaLoader struct{}

func (runtimeSchemaLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("external schema reference %q is unavailable; use a local $defs reference", url)
}

func validateJSONValue(value any, raw json.RawMessage, path, code string) error {
	failure := &ValueValidationFailure{Code: code, Path: path, Expected: raw}
	if !json.Valid(raw) {
		failure.Expected, _ = json.Marshal(string(raw))
	}
	actual, err := json.Marshal(value)
	if err != nil {
		failure.Message = fmt.Sprintf("value is not JSON: %v", err)
		return failure
	}
	failure.Actual = actual
	if !json.Valid(raw) {
		failure.Code = InvalidSchema
		failure.Message = "invalid JSON Schema: expected exactly one JSON value"
		return failure
	}
	var schema any
	schemaDecoder := json.NewDecoder(bytes.NewReader(raw))
	schemaDecoder.UseNumber()
	if err := schemaDecoder.Decode(&schema); err != nil {
		failure.Code = InvalidSchema
		failure.Message = fmt.Sprintf("invalid JSON Schema: %v", err)
		return failure
	}
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(runtimeSchemaLoader{})
	const location = "https://muxlio.local/runtime-schema.json"
	if err := compiler.AddResource(location, schema); err != nil {
		failure.Code = InvalidSchema
		failure.Message = err.Error()
		return failure
	}
	compiled, err := compiler.Compile(location)
	if err != nil {
		failure.Code = InvalidSchema
		failure.Message = err.Error()
		return failure
	}
	// Normalize Go values to the JSON data model, preserving number precision.
	dec := json.NewDecoder(bytes.NewReader(actual))
	dec.UseNumber()
	var normalized any
	if err := dec.Decode(&normalized); err != nil {
		failure.Message = err.Error()
		return failure
	}
	if err := compiled.Validate(normalized); err != nil {
		failure.Message = err.Error()
		return failure
	}
	return nil
}

// Preserve the run-input convention that required string IDs in collections
// identify distinct work items. This is additional to JSON Schema validation.
func validateUniqueInputIDs(value any, schema map[string]any, path string) error {
	if obj, ok := value.(map[string]any); ok {
		props, _ := schema["properties"].(map[string]any)
		for name, val := range obj {
			prop, _ := props[name].(map[string]any)
			if err := validateUniqueInputIDs(val, prop, path+"/"+name); err != nil {
				return err
			}
		}
	}
	if arr, ok := value.([]any); ok {
		items, _ := schema["items"].(map[string]any)
		props, _ := items["properties"].(map[string]any)
		idSchema, _ := props["id"].(map[string]any)
		track := false
		required, _ := items["required"].([]any)
		for _, name := range required {
			if name == "id" && idSchema["type"] == "string" {
				track = true
			}
		}
		seen := map[string]bool{}
		for i, item := range arr {
			if track {
				obj, _ := item.(map[string]any)
				id, _ := obj["id"].(string)
				if seen[id] {
					return fmt.Errorf("muxlio: input %s has duplicate id %q", path, id)
				}
				seen[id] = true
			}
			if err := validateUniqueInputIDs(item, items, fmt.Sprintf("%s/%d", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}
