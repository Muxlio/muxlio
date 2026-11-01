package plan

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
)

func validateInputAgainstSchema(value any, schema map[string]any, path string) error {
	if len(schema) == 0 {
		return nil
	}
	if anyOf, ok := schema["anyOf"].([]any); ok {
		var lastErr error
		for _, opt := range anyOf {
			optSchema, ok := opt.(map[string]any)
			if !ok {
				continue
			}
			if err := validateInputAgainstSchema(value, optSchema, path); err == nil {
				return nil
			} else {
				lastErr = err
			}
		}
		if lastErr != nil {
			return lastErr
		}
		return fmt.Errorf("muxlio: input %s does not match schema", pathLabel(path))
	}

	typ, _ := schema["type"].(string)
	switch typ {
	case "object":
		return validateObjectInput(value, schema, path)
	case "array":
		return validateArrayInput(value, schema, path)
	case "string":
		return validateStringInput(value, schema, path)
	case "number":
		return validateNumberInput(value, schema, path, false)
	case "integer":
		return validateNumberInput(value, schema, path, true)
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("muxlio: input %s must be a boolean", pathLabel(path))
		}
	case "":
		// unconstrained
	default:
		return fmt.Errorf("muxlio: unsupported input schema type %q at %s", typ, pathLabel(path))
	}
	return nil
}

func validateObjectInput(value any, schema map[string]any, path string) error {
	obj, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("muxlio: input %s must be an object", pathLabel(path))
	}
	if required, ok := schema["required"].([]any); ok {
		for _, req := range required {
			name, ok := req.(string)
			if !ok {
				continue
			}
			if _, ok := obj[name]; !ok {
				return fmt.Errorf("muxlio: input missing required field %q", joinPath(path, name))
			}
		}
	}
	props, _ := schema["properties"].(map[string]any)
	for name, val := range obj {
		propSchema, ok := props[name].(map[string]any)
		if !ok {
			continue
		}
		if err := validateInputAgainstSchema(val, propSchema, joinPath(path, name)); err != nil {
			return err
		}
	}
	return nil
}

func validateArrayInput(value any, schema map[string]any, path string) error {
	arr, ok := value.([]any)
	if !ok {
		return fmt.Errorf("muxlio: input %s must be an array", pathLabel(path))
	}
	if minItems, ok := intConstraint(schema["minItems"]); ok && len(arr) < minItems {
		return fmt.Errorf("muxlio: input %s must have at least %d item(s)", pathLabel(path), minItems)
	}
	if maxItems, ok := intConstraint(schema["maxItems"]); ok && len(arr) > maxItems {
		return fmt.Errorf("muxlio: input %s must have at most %d item(s)", pathLabel(path), maxItems)
	}
	itemsSchema, _ := schema["items"].(map[string]any)
	if itemsSchema == nil {
		return nil
	}
	seenIDs := map[string]struct{}{}
	idRequired := false
	if req, ok := itemsSchema["required"].([]any); ok {
		for _, r := range req {
			if s, ok := r.(string); ok && s == "id" {
				idRequired = true
				break
			}
		}
	}
	idSchema, _ := itemsSchema["properties"].(map[string]any)["id"].(map[string]any)
	trackUniqueIDs := idRequired && idSchema != nil && idSchema["type"] == "string"

	for i, item := range arr {
		itemPath := fmt.Sprintf("%s[%d]", pathLabel(path), i)
		if err := validateInputAgainstSchema(item, itemsSchema, itemPath); err != nil {
			return err
		}
		if trackUniqueIDs {
			obj, ok := item.(map[string]any)
			if !ok {
				continue
			}
			idVal, ok := obj["id"].(string)
			if !ok {
				continue
			}
			if _, dup := seenIDs[idVal]; dup {
				return fmt.Errorf("muxlio: input %s has duplicate id %q", pathLabel(path), idVal)
			}
			seenIDs[idVal] = struct{}{}
		}
	}
	return nil
}

func validateStringInput(value any, schema map[string]any, path string) error {
	s, ok := value.(string)
	if !ok {
		return fmt.Errorf("muxlio: input %s must be a string", pathLabel(path))
	}
	if minLen, ok := intConstraint(schema["minLength"]); ok && len(s) < minLen {
		return fmt.Errorf("muxlio: input %s must be at least %d character(s)", pathLabel(path), minLen)
	}
	if maxLen, ok := intConstraint(schema["maxLength"]); ok && len(s) > maxLen {
		return fmt.Errorf("muxlio: input %s must be at most %d character(s)", pathLabel(path), maxLen)
	}
	if enum, ok := schema["enum"].([]any); ok {
		for _, opt := range enum {
			if reflect.DeepEqual(opt, s) {
				return nil
			}
		}
		return fmt.Errorf("muxlio: input %s has invalid value", pathLabel(path))
	}
	if c, ok := schema["const"]; ok && !reflect.DeepEqual(c, s) {
		return fmt.Errorf("muxlio: input %s has invalid value", pathLabel(path))
	}
	return nil
}

func validateNumberInput(value any, schema map[string]any, path string, integer bool) error {
	num, ok := jsonNumber(value)
	if !ok {
		return fmt.Errorf("muxlio: input %s must be a number", pathLabel(path))
	}
	if integer && math.Mod(num, 1) != 0 {
		return fmt.Errorf("muxlio: input %s must be an integer", pathLabel(path))
	}
	if min, ok := floatConstraint(schema["minimum"]); ok && num < min {
		return fmt.Errorf("muxlio: input %s must be >= %v", pathLabel(path), min)
	}
	if max, ok := floatConstraint(schema["maximum"]); ok && num > max {
		return fmt.Errorf("muxlio: input %s must be <= %v", pathLabel(path), max)
	}
	return nil
}

func intConstraint(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, false
		}
		return int(i), true
	default:
		return 0, false
	}
}

func floatConstraint(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

func jsonNumber(value any) (float64, bool) {
	switch n := value.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func pathLabel(path string) string {
	if path == "" {
		return "input"
	}
	return path
}

func joinPath(base, field string) string {
	if base == "" {
		return field
	}
	return base + "." + field
}
