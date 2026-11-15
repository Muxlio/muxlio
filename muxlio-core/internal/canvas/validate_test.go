package canvas

import (
	"encoding/json"
	"strings"
	"testing"
)

const illustrativeDocument = `{
  "version": 1,
  "title": "Execution Quality Review",
  "description": "Comparison of execution outcomes across a selected period.",
  "sources": {
    "observations": { "artifactPort": "metrics" }
  },
  "content": {
    "type": "stack",
    "id": "root",
    "gap": "comfortable",
    "children": [
      {
        "type": "heading",
        "id": "title",
        "level": 1,
        "text": "Execution Quality Review"
      },
      {
        "type": "grid",
        "id": "headline",
        "columns": 3,
        "children": [
          { "type": "metric", "id": "runs", "label": "Total runs", "value": 248 },
          { "type": "metric", "id": "success", "label": "Success rate", "value": "96.4%" },
          { "type": "metric", "id": "duration", "label": "Average duration", "value": "42 s" }
        ]
      },
      {
        "type": "select",
        "id": "period",
        "label": "Period",
        "initial": "7d",
        "options": [
          { "value": "7d", "label": "Last 7 days" },
          { "value": "30d", "label": "Last 30 days" }
        ]
      },
      {
        "type": "barChart",
        "id": "volume",
        "title": "Runs by day",
        "data": {
          "source": "observations",
          "pointer": "/daily",
          "filter": {
            "field": "period",
            "equalsState": "period"
          }
        },
        "categoryField": "day",
        "valueField": "runs"
      },
      {
        "type": "table",
        "id": "details",
        "title": "Per-executor performance",
        "data": { "source": "observations", "pointer": "/executors" },
        "columns": [
          { "field": "executor", "label": "Executor", "kind": "text" },
          { "field": "runs", "label": "Runs", "kind": "integer" },
          { "field": "successRate", "label": "Success rate", "kind": "percent" }
        ],
        "sortable": true,
        "pageSize": 25
      },
      {
        "type": "markdown",
        "id": "commentary",
        "text": "## Notes\nFigures are based on the workflow-generated data file."
      }
    ]
  }
}`

func TestValidateIllustrativeDocument(t *testing.T) {
	res, err := Validate([]byte(illustrativeDocument))
	if err != nil {
		t.Fatalf("unexpected validation error: %v (errors: %+v)", err, res.Errors)
	}
	if !res.Valid {
		t.Fatalf("expected valid document, got false")
	}
	if res.Components != 10 {
		t.Errorf("expected 10 components, got %d", res.Components)
	}
	if res.Depth != 3 {
		t.Errorf("expected depth 3, got %d", res.Depth)
	}
}

func TestValidateAllCatalogExamples(t *testing.T) {
	cat := Catalog()
	for _, spec := range cat {
		doc := map[string]any{
			"version": 1,
			"title":   "Test " + spec.Type,
			"content": spec.Example,
		}
		// If example uses data source, declare it
		if spec.Type == "table" || spec.Type == "barChart" || spec.Type == "json" {
			doc["sources"] = map[string]any{
				"observations": map[string]any{"artifactPort": "metrics"},
			}
		}
		bytes, err := json.Marshal(doc)
		if err != nil {
			t.Fatalf("failed to marshal doc for %s: %v", spec.Type, err)
		}
		res, err := Validate(bytes)
		if err != nil {
			t.Errorf("component %q example failed validation: %v (errors: %+v)", spec.Type, err, res.Errors)
		}
	}
}

func TestValidateRejectsUnsupportedVersion(t *testing.T) {
	doc := strings.Replace(illustrativeDocument, `"version": 1`, `"version": 2`, 1)
	_, err := Validate([]byte(doc))
	if err == nil {
		t.Fatal("expected error for version 2, got nil")
	}
}

func TestValidateRejectsDuplicateIDs(t *testing.T) {
	doc := strings.Replace(illustrativeDocument, `"id": "headline"`, `"id": "root"`, 1)
	_, err := Validate([]byte(doc))
	if err == nil {
		t.Fatal("expected error for duplicate ID 'root', got nil")
	}
	if !strings.Contains(err.Error(), "duplicate component id") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestValidateRejectsPrototypeKeys(t *testing.T) {
	doc := strings.Replace(illustrativeDocument, `"id": "headline"`, `"__proto__": "danger", "id": "headline"`, 1)
	_, err := Validate([]byte(doc))
	if err == nil {
		t.Fatal("expected error for __proto__ key, got nil")
	}
	if !strings.Contains(err.Error(), "forbidden property name") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestValidateRejectsUndeclaredSource(t *testing.T) {
	doc := strings.Replace(illustrativeDocument, `"source": "observations"`, `"source": "unknown"`, 1)
	_, err := Validate([]byte(doc))
	if err == nil {
		t.Fatal("expected error for undeclared source, got nil")
	}
	if !strings.Contains(err.Error(), "referenced source \"unknown\" is not defined") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestValidateRejectsUndeclaredEqualsState(t *testing.T) {
	doc := strings.Replace(illustrativeDocument, `"equalsState": "period"`, `"equalsState": "nonexistent"`, 1)
	_, err := Validate([]byte(doc))
	if err == nil {
		t.Fatal("expected error for nonexistent equalsState, got nil")
	}
	if !strings.Contains(err.Error(), "state variable \"nonexistent\" referenced in equalsState does not exist") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestValidateRejectsOversizedInlineData(t *testing.T) {
	hugeString := strings.Repeat("A", 300*1024)
	doc := map[string]any{
		"version": 1,
		"title":   "Oversized",
		"sources": map[string]any{
			"big": map[string]any{"value": hugeString},
		},
		"content": map[string]any{
			"type":  "heading",
			"id":    "h1",
			"level": 1,
			"text":  "Hello",
		},
	}
	bytes, _ := json.Marshal(doc)
	_, err := Validate(bytes)
	if err == nil {
		t.Fatal("expected error for oversized inline data, got nil")
	}
	if !strings.Contains(err.Error(), "inline source data size") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateRejectsExcessiveDepth(t *testing.T) {
	// Build tree depth > 16
	var curr map[string]any
	for i := 20; i >= 1; i-- {
		node := map[string]any{
			"type": "stack",
			"id":   strings.Repeat("a", i),
		}
		if curr == nil {
			node["children"] = []any{
				map[string]any{
					"type": "text",
					"id":   "leaf",
					"text": "deep",
				},
			}
		} else {
			node["children"] = []any{curr}
		}
		curr = node
	}
	doc := map[string]any{
		"version": 1,
		"title":   "Deep Tree",
		"content": curr,
	}
	bytes, _ := json.Marshal(doc)
	_, err := Validate(bytes)
	if err == nil {
		t.Fatal("expected error for tree depth > 16, got nil")
	}
	if !strings.Contains(err.Error(), "component tree depth") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCatalogAndGetComponent(t *testing.T) {
	cat := Catalog()
	if len(cat) != 23 {
		t.Fatalf("expected 23 components in catalog, got %d", len(cat))
	}
	spec, err := GetComponent("table")
	if err != nil {
		t.Fatalf("failed to get table component: %v", err)
	}
	if spec.Category != "Data" {
		t.Errorf("expected Category Data, got %q", spec.Category)
	}
	_, err = GetComponent("unknown")
	if err == nil {
		t.Error("expected error for unknown component, got nil")
	}
}

func TestValidateRejectsTrailingJSON(t *testing.T) {
	for _, trailing := range []string{"}", "]", "{}", "null", "garbage"} {
		if result, err := Validate([]byte(illustrativeDocument + trailing)); err == nil || result.Valid {
			t.Errorf("accepted trailing data %q", trailing)
		}
	}
	if result, err := Validate([]byte(illustrativeDocument + "\n\t ")); err != nil || !result.Valid {
		t.Fatalf("rejected trailing whitespace: %v", err)
	}
}
