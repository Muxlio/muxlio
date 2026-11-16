package canvas

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed schema/v1.json
var schemaV1Bytes []byte

const (
	MaxDocumentSizeBytes = 1024 * 1024 // 1 MiB
	MaxInlineDataBytes   = 256 * 1024  // 256 KiB
	MaxComponentCount    = 500
	MaxTreeDepth         = 16
	MaxJSONNesting       = 128
)

var (
	compiledSchema *jsonschema.Schema
	schemaInitOnce sync.Once
	schemaInitErr  error

	dataRefTypesOnce sync.Once
	dataRefTypes     map[string]bool
)

// componentDataIsRef reports whether the schema allows a dataRef in the
// component's data property. Other components (json) carry arbitrary data.
func componentDataIsRef(nodeType string) bool {
	dataRefTypesOnce.Do(func() {
		dataRefTypes = map[string]bool{}
		var schema struct {
			Defs struct {
				Node struct {
					AnyOf []struct {
						Properties struct {
							Type struct {
								Const string `json:"const"`
							} `json:"type"`
							Data json.RawMessage `json:"data"`
						} `json:"properties"`
					} `json:"anyOf"`
				} `json:"node"`
			} `json:"$defs"`
		}
		if json.Unmarshal(schemaV1Bytes, &schema) != nil {
			return
		}
		for _, variant := range schema.Defs.Node.AnyOf {
			if bytes.Contains(variant.Properties.Data, []byte(`"#/$defs/dataRef"`)) {
				dataRefTypes[variant.Properties.Type.Const] = true
			}
		}
	})
	return dataRefTypes[nodeType]
}

func getSchema() (*jsonschema.Schema, error) {
	schemaInitOnce.Do(func() {
		var raw any
		dec := json.NewDecoder(bytes.NewReader(schemaV1Bytes))
		dec.UseNumber()
		if err := dec.Decode(&raw); err != nil {
			schemaInitErr = fmt.Errorf("failed to decode embedded canvas schema: %w", err)
			return
		}
		compiler := jsonschema.NewCompiler()
		const url = "https://muxlio.dev/schemas/canvas/v1.json"
		if err := compiler.AddResource(url, raw); err != nil {
			schemaInitErr = fmt.Errorf("failed to add canvas schema resource: %w", err)
			return
		}
		compiled, err := compiler.Compile(url)
		if err != nil {
			schemaInitErr = fmt.Errorf("failed to compile canvas schema: %w", err)
			return
		}
		compiledSchema = compiled
	})
	return compiledSchema, schemaInitErr
}

// ValidationError describes a specific validation failure with field path.
type ValidationError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

func (e ValidationError) Error() string {
	if e.Path != "" {
		return fmt.Sprintf("%s: %s", e.Path, e.Message)
	}
	return e.Message
}

// ValidationResult summarizes the validation of a Canvas document.
type ValidationResult struct {
	Valid      bool              `json:"valid"`
	Components int               `json:"components"`
	Depth      int               `json:"depth"`
	Errors     []ValidationError `json:"errors,omitempty"`
}

// Validate validates a raw Canvas JSON document against the Canvas v1 specification.
func Validate(data []byte) (*ValidationResult, error) {
	res := &ValidationResult{Valid: false}

	if len(data) > MaxDocumentSizeBytes {
		err := ValidationError{
			Path:    "/",
			Message: fmt.Sprintf("document size %d bytes exceeds maximum allowed limit of %d bytes (1 MiB)", len(data), MaxDocumentSizeBytes),
		}
		res.Errors = append(res.Errors, err)
		return res, err
	}

	if len(bytes.TrimSpace(data)) == 0 {
		err := ValidationError{Path: "/", Message: "document is empty"}
		res.Errors = append(res.Errors, err)
		return res, err
	}

	var parsed any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&parsed); err != nil {
		verr := ValidationError{Path: "/", Message: "invalid JSON: " + err.Error()}
		res.Errors = append(res.Errors, verr)
		return res, verr
	}

	// Reject extra trailing tokens
	if err := dec.Decode(new(any)); err != io.EOF {
		verr := ValidationError{Path: "/", Message: "unexpected trailing data after JSON document"}
		res.Errors = append(res.Errors, verr)
		return res, verr
	}

	docObj, ok := parsed.(map[string]any)
	if !ok {
		verr := ValidationError{Path: "/", Message: "root document must be a JSON object"}
		res.Errors = append(res.Errors, verr)
		return res, verr
	}

	// Prototype pollution key check
	if err := checkNoProtoKeys(parsed, "", 0); err != nil {
		res.Errors = append(res.Errors, *err)
		return res, *err
	}

	// Validate sources bounds
	declaredSources := make(map[string]struct{})
	if sourcesRaw, ok := docObj["sources"].(map[string]any); ok {
		for sName, sVal := range sourcesRaw {
			declaredSources[sName] = struct{}{}
			if sObj, ok := sVal.(map[string]any); ok {
				if inlineVal, ok := sObj["value"]; ok {
					inlineBytes, _ := json.Marshal(inlineVal)
					if len(inlineBytes) > MaxInlineDataBytes {
						verr := ValidationError{
							Path:    fmt.Sprintf("/sources/%s/value", sName),
							Message: fmt.Sprintf("inline source data size %d bytes exceeds maximum limit of %d bytes (256 KiB)", len(inlineBytes), MaxInlineDataBytes),
						}
						res.Errors = append(res.Errors, verr)
						return res, verr
					}
				}
			}
		}
	}

	// Collect declared controls and component IDs
	idSet := make(map[string]string) // id -> path
	stateIDs := make(map[string]struct{})

	if controlsRaw, ok := docObj["controls"].(map[string]any); ok {
		for cName, cVal := range controlsRaw {
			if cObj, ok := cVal.(map[string]any); ok {
				id, _ := cObj["id"].(string)
				path := fmt.Sprintf("/controls/%s", cName)
				if id == "" {
					id = cName
				}
				if prev, exists := idSet[id]; exists {
					verr := ValidationError{
						Path:    path,
						Message: fmt.Sprintf("duplicate control/component id %q (already defined at %s)", id, prev),
					}
					res.Errors = append(res.Errors, verr)
					return res, verr
				}
				idSet[id] = path
				stateIDs[id] = struct{}{}
			}
		}
	}

	contentNode, ok := docObj["content"].(map[string]any)
	if !ok {
		verr := ValidationError{Path: "/content", Message: "content root must be a component node object"}
		res.Errors = append(res.Errors, verr)
		return res, verr
	}

	componentCount := 0
	maxDepthFound := 0

	var walkNode func(node map[string]any, currentDepth int, currentPath string) *ValidationError
	walkNode = func(node map[string]any, currentDepth int, currentPath string) *ValidationError {
		componentCount++
		if componentCount > MaxComponentCount {
			return &ValidationError{
				Path:    currentPath,
				Message: fmt.Sprintf("component count exceeded maximum limit of %d", MaxComponentCount),
			}
		}
		if currentDepth > maxDepthFound {
			maxDepthFound = currentDepth
		}
		if currentDepth > MaxTreeDepth {
			return &ValidationError{
				Path:    currentPath,
				Message: fmt.Sprintf("component tree depth %d exceeds maximum allowed depth of %d", currentDepth, MaxTreeDepth),
			}
		}

		id, _ := node["id"].(string)
		if id == "" {
			return &ValidationError{
				Path:    currentPath + "/id",
				Message: "component id is required",
			}
		}
		if prev, exists := idSet[id]; exists {
			return &ValidationError{
				Path:    currentPath + "/id",
				Message: fmt.Sprintf("duplicate component id %q (already defined at %s)", id, prev),
			}
		}
		idSet[id] = currentPath

		nodeType, _ := node["type"].(string)
		if nodeType == "select" || nodeType == "toggle" || nodeType == "search" {
			stateIDs[id] = struct{}{}
		}

		if dataProp, ok := node["data"]; ok && componentDataIsRef(nodeType) {
			if dataRef, ok := dataProp.(map[string]any); ok {
				if src, ok := dataRef["source"].(string); ok {
					if _, declared := declaredSources[src]; !declared {
						return &ValidationError{
							Path:    currentPath + "/data/source",
							Message: fmt.Sprintf("referenced source %q is not defined in top-level sources", src),
						}
					}
				}
				if ptr, ok := dataRef["pointer"].(string); ok && ptr != "" {
					if !strings.HasPrefix(ptr, "/") {
						return &ValidationError{
							Path:    currentPath + "/data/pointer",
							Message: fmt.Sprintf("JSON pointer %q must start with '/'", ptr),
						}
					}
					// Check for dangerous traversal
					segments := strings.Split(ptr, "/")
					for _, seg := range segments {
						if seg == "__proto__" || seg == "prototype" || seg == "constructor" {
							return &ValidationError{
								Path:    currentPath + "/data/pointer",
								Message: fmt.Sprintf("JSON pointer %q contains forbidden prototype property %q", ptr, seg),
							}
						}
					}
				}
			}
		}

		// Recurse children
		if children, ok := node["children"].([]any); ok {
			for i, ch := range children {
				if chObj, ok := ch.(map[string]any); ok {
					if err := walkNode(chObj, currentDepth+1, fmt.Sprintf("%s/children/%d", currentPath, i)); err != nil {
						return err
					}
				}
			}
		}

		// Recurse tabs
		if tabs, ok := node["tabs"].([]any); ok {
			for i, t := range tabs {
				if tObj, ok := t.(map[string]any); ok {
					tID, _ := tObj["id"].(string)
					tPath := fmt.Sprintf("%s/tabs/%d", currentPath, i)
					if tID != "" {
						if prev, exists := idSet[tID]; exists {
							return &ValidationError{
								Path:    tPath + "/id",
								Message: fmt.Sprintf("duplicate tab id %q (already defined at %s)", tID, prev),
							}
						}
						idSet[tID] = tPath
					}
					if tabChildren, ok := tObj["children"].([]any); ok {
						for j, ch := range tabChildren {
							if chObj, ok := ch.(map[string]any); ok {
								if err := walkNode(chObj, currentDepth+1, fmt.Sprintf("%s/children/%d", tPath, j)); err != nil {
									return err
								}
							}
						}
					}
				}
			}
		}

		return nil
	}

	if err := walkNode(contentNode, 1, "/content"); err != nil {
		res.Errors = append(res.Errors, *err)
		return res, *err
	}

	// Validate against JSON schema
	schema, err := getSchema()
	if err != nil {
		return nil, err
	}
	if err := schema.Validate(parsed); err != nil {
		verr := ValidationError{Path: "/", Message: err.Error()}
		res.Errors = append(res.Errors, verr)
		return res, verr
	}

	// Second pass: check filter.equalsState references
	var checkFilters func(node map[string]any, path string) *ValidationError
	checkFilters = func(node map[string]any, path string) *ValidationError {
		nodeType, _ := node["type"].(string)
		if dataProp, ok := node["data"]; ok && componentDataIsRef(nodeType) {
			if dataRef, ok := dataProp.(map[string]any); ok {
				if filterObj, ok := dataRef["filter"].(map[string]any); ok {
					if eqState, ok := filterObj["equalsState"].(string); ok && eqState != "" {
						if _, exists := stateIDs[eqState]; !exists {
							return &ValidationError{
								Path:    path + "/data/filter/equalsState",
								Message: fmt.Sprintf("state variable %q referenced in equalsState does not exist as a control id", eqState),
							}
						}
					}
				}
			}
		}
		if children, ok := node["children"].([]any); ok {
			for i, ch := range children {
				if chObj, ok := ch.(map[string]any); ok {
					if err := checkFilters(chObj, fmt.Sprintf("%s/children/%d", path, i)); err != nil {
						return err
					}
				}
			}
		}
		if tabs, ok := node["tabs"].([]any); ok {
			for i, t := range tabs {
				if tObj, ok := t.(map[string]any); ok {
					tPath := fmt.Sprintf("%s/tabs/%d", path, i)
					if tabChildren, ok := tObj["children"].([]any); ok {
						for j, ch := range tabChildren {
							if chObj, ok := ch.(map[string]any); ok {
								if err := checkFilters(chObj, fmt.Sprintf("%s/children/%d", tPath, j)); err != nil {
									return err
								}
							}
						}
					}
				}
			}
		}
		return nil
	}

	if err := checkFilters(contentNode, "/content"); err != nil {
		res.Errors = append(res.Errors, *err)
		return res, *err
	}

	res.Valid = true
	res.Components = componentCount
	res.Depth = maxDepthFound
	return res, nil
}

func checkNoProtoKeys(v any, path string, depth int) *ValidationError {
	if depth > MaxJSONNesting {
		return &ValidationError{
			Path:    path,
			Message: fmt.Sprintf("JSON nesting exceeds maximum depth of %d", MaxJSONNesting),
		}
	}
	switch val := v.(type) {
	case map[string]any:
		for k, item := range val {
			if k == "__proto__" || k == "prototype" || k == "constructor" {
				return &ValidationError{
					Path:    path + "/" + k,
					Message: fmt.Sprintf("forbidden property name %q", k),
				}
			}
			if err := checkNoProtoKeys(item, path+"/"+k, depth+1); err != nil {
				return err
			}
		}
	case []any:
		for i, item := range val {
			if err := checkNoProtoKeys(item, fmt.Sprintf("%s/%d", path, i), depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}
