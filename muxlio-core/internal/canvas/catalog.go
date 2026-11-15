package canvas

import (
	"fmt"
)

// ComponentSpec describes an allowed Canvas component in the public vocabulary.
type ComponentSpec struct {
	Type            string         `json:"type"`
	Version         int            `json:"version"`
	Category        string         `json:"category"`
	Description     string         `json:"description"`
	PropsSchema     map[string]any `json:"propsSchema,omitempty"`
	SupportedEvents []string       `json:"supportedEvents,omitempty"`
	AllowedChildren []string       `json:"allowedChildren,omitempty"`
	Example         map[string]any `json:"example,omitempty"`
}

var catalog = []ComponentSpec{
	// Layout
	{
		Type:            "stack",
		Version:         1,
		Category:        "Layout",
		Description:     "Arranges child components vertically with consistent spacing and alignment.",
		SupportedEvents: []string{},
		AllowedChildren: []string{"*"},
		PropsSchema: map[string]any{
			"gap":   map[string]any{"type": "string", "enum": []string{"none", "compact", "comfortable", "spacious"}},
			"align": map[string]any{"type": "string", "enum": []string{"start", "center", "end", "stretch"}},
		},
		Example: map[string]any{
			"type":     "stack",
			"id":       "summaryStack",
			"gap":      "comfortable",
			"children": []any{},
		},
	},
	{
		Type:            "grid",
		Version:         1,
		Category:        "Layout",
		Description:     "Multi-column responsive grid layout.",
		SupportedEvents: []string{},
		AllowedChildren: []string{"*"},
		PropsSchema: map[string]any{
			"columns": map[string]any{"description": "Number of columns (1-12) or 'auto-fit'"},
			"gap":     map[string]any{"type": "string", "enum": []string{"none", "compact", "comfortable", "spacious"}},
		},
		Example: map[string]any{
			"type":     "grid",
			"id":       "metricsGrid",
			"columns":  3,
			"gap":      "comfortable",
			"children": []any{},
		},
	},
	{
		Type:            "row",
		Version:         1,
		Category:        "Layout",
		Description:     "Arranges child components horizontally with flexible alignment.",
		SupportedEvents: []string{},
		AllowedChildren: []string{"*"},
		PropsSchema: map[string]any{
			"gap":     map[string]any{"type": "string", "enum": []string{"none", "compact", "comfortable", "spacious"}},
			"align":   map[string]any{"type": "string", "enum": []string{"start", "center", "end", "stretch"}},
			"justify": map[string]any{"type": "string", "enum": []string{"start", "center", "end", "between", "around"}},
			"wrap":    map[string]any{"type": "boolean"},
		},
		Example: map[string]any{
			"type":     "row",
			"id":       "actionRow",
			"gap":      "compact",
			"children": []any{},
		},
	},
	{
		Type:            "section",
		Version:         1,
		Category:        "Layout",
		Description:     "Grouped container with an optional title, description, and collapsible disclosure.",
		SupportedEvents: []string{"toggleSection"},
		AllowedChildren: []string{"*"},
		PropsSchema: map[string]any{
			"title":       map[string]any{"type": "string"},
			"description": map[string]any{"type": "string"},
			"collapsible": map[string]any{"type": "boolean"},
			"collapsed":   map[string]any{"type": "boolean"},
		},
		Example: map[string]any{
			"type":        "section",
			"id":          "detailsSection",
			"title":       "Detailed Analysis",
			"collapsible": true,
			"children":    []any{},
		},
	},
	{
		Type:            "tabs",
		Version:         1,
		Category:        "Layout",
		Description:     "Tabbed layout switching between labeled panels of child components.",
		SupportedEvents: []string{"selectTab"},
		AllowedChildren: []string{"*"},
		PropsSchema: map[string]any{
			"initial": map[string]any{"type": "string", "description": "Tab ID to activate initially"},
			"tabs":    map[string]any{"type": "array", "description": "List of {id, label, children} objects"},
		},
		Example: map[string]any{
			"type":    "tabs",
			"id":      "mainTabs",
			"initial": "overview",
			"tabs": []any{
				map[string]any{
					"id":       "overview",
					"label":    "Overview",
					"children": []any{},
				},
			},
		},
	},
	{
		Type:            "divider",
		Version:         1,
		Category:        "Layout",
		Description:     "Visual separator line with configurable spacing.",
		SupportedEvents: []string{},
		AllowedChildren: []string{},
		PropsSchema: map[string]any{
			"spacing": map[string]any{"type": "string", "enum": []string{"none", "compact", "comfortable", "spacious"}},
		},
		Example: map[string]any{
			"type":    "divider",
			"id":      "sep1",
			"spacing": "comfortable",
		},
	},

	// Reading
	{
		Type:            "heading",
		Version:         1,
		Category:        "Reading",
		Description:     "Structured section heading (levels 1 through 6).",
		SupportedEvents: []string{},
		AllowedChildren: []string{},
		PropsSchema: map[string]any{
			"level": map[string]any{"type": "integer", "minimum": 1, "maximum": 6},
			"text":  map[string]any{"type": "string"},
		},
		Example: map[string]any{
			"type":  "heading",
			"id":    "hdr1",
			"level": 2,
			"text":  "Execution Summary",
		},
	},
	{
		Type:            "text",
		Version:         1,
		Category:        "Reading",
		Description:     "Formatted plain text block with typography variants.",
		SupportedEvents: []string{},
		AllowedChildren: []string{},
		PropsSchema: map[string]any{
			"text":    map[string]any{"type": "string"},
			"variant": map[string]any{"type": "string", "enum": []string{"body", "caption", "lead", "code", "muted"}},
		},
		Example: map[string]any{
			"type":    "text",
			"id":      "leadText",
			"text":    "All benchmark runs completed within the target SLAs.",
			"variant": "lead",
		},
	},
	{
		Type:            "markdown",
		Version:         1,
		Category:        "Reading",
		Description:     "Safe Markdown rendering using Muxlio's sandboxed parser.",
		SupportedEvents: []string{},
		AllowedChildren: []string{},
		PropsSchema: map[string]any{
			"text": map[string]any{"type": "string"},
		},
		Example: map[string]any{
			"type": "markdown",
			"id":   "notes",
			"text": "### Observations\n- P99 latency reduced by **14%**.\n- No transient errors observed.",
		},
	},
	{
		Type:            "code",
		Version:         1,
		Category:        "Reading",
		Description:     "Syntax-styled or preformatted code snippet.",
		SupportedEvents: []string{},
		AllowedChildren: []string{},
		PropsSchema: map[string]any{
			"text":     map[string]any{"type": "string"},
			"language": map[string]any{"type": "string"},
		},
		Example: map[string]any{
			"type":     "code",
			"id":       "configSnippet",
			"text":     "{\"timeout\": 300, \"retries\": 3}",
			"language": "json",
		},
	},
	{
		Type:            "callout",
		Version:         1,
		Category:        "Reading",
		Description:     "Prominent notice card with tone (info, success, warning, error).",
		SupportedEvents: []string{},
		AllowedChildren: []string{},
		PropsSchema: map[string]any{
			"tone":  map[string]any{"type": "string", "enum": []string{"info", "success", "warning", "error"}},
			"title": map[string]any{"type": "string"},
			"text":  map[string]any{"type": "string"},
		},
		Example: map[string]any{
			"type":  "callout",
			"id":    "alert1",
			"tone":  "success",
			"title": "Pass Rate Goal Met",
			"text":  "Quality checks passed across all target suites.",
		},
	},

	// Facts
	{
		Type:            "metric",
		Version:         1,
		Category:        "Facts",
		Description:     "Key performance indicator or single metric card.",
		SupportedEvents: []string{},
		AllowedChildren: []string{},
		PropsSchema: map[string]any{
			"label": map[string]any{"type": "string"},
			"value": map[string]any{"type": []string{"string", "number"}},
			"note":  map[string]any{"type": "string"},
			"tone":  map[string]any{"type": "string", "enum": []string{"green", "red", "amber", "neutral"}},
		},
		Example: map[string]any{
			"type":  "metric",
			"id":    "successRate",
			"label": "Success Rate",
			"value": "99.2%",
			"note":  "+2.1% from prior baseline",
			"tone":  "green",
		},
	},
	{
		Type:            "keyValue",
		Version:         1,
		Category:        "Facts",
		Description:     "Label-value definition row.",
		SupportedEvents: []string{},
		AllowedChildren: []string{},
		PropsSchema: map[string]any{
			"label": map[string]any{"type": "string"},
			"value": map[string]any{"type": []string{"string", "number", "boolean"}},
			"note":  map[string]any{"type": "string"},
		},
		Example: map[string]any{
			"type":  "keyValue",
			"id":    "envKV",
			"label": "Environment",
			"value": "production-us-east",
		},
	},
	{
		Type:            "status",
		Version:         1,
		Category:        "Facts",
		Description:     "Status badge indicating run or lifecycle state.",
		SupportedEvents: []string{},
		AllowedChildren: []string{},
		PropsSchema: map[string]any{
			"label": map[string]any{"type": "string"},
			"state": map[string]any{"type": "string"},
			"tone":  map[string]any{"type": "string", "enum": []string{"green", "red", "amber", "neutral", "blue"}},
		},
		Example: map[string]any{
			"type":  "status",
			"id":    "runStatus",
			"label": "Ready",
			"state": "succeeded",
			"tone":  "green",
		},
	},
	{
		Type:            "summary",
		Version:         1,
		Category:        "Facts",
		Description:     "Collection of concise summary statistics cards.",
		SupportedEvents: []string{},
		AllowedChildren: []string{},
		PropsSchema: map[string]any{
			"items": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"label": map[string]any{"type": "string"},
						"value": map[string]any{"type": []string{"string", "number"}},
						"tone":  map[string]any{"type": "string", "enum": []string{"green", "red", "amber", "neutral"}},
					},
				},
			},
		},
		Example: map[string]any{
			"type": "summary",
			"id":   "execSummary",
			"items": []any{
				map[string]any{"label": "Total Tasks", "value": 142},
				map[string]any{"label": "Completed", "value": 140, "tone": "green"},
				map[string]any{"label": "Retried", "value": 2, "tone": "amber"},
			},
		},
	},

	// Data
	{
		Type:            "table",
		Version:         1,
		Category:        "Data",
		Description:     "Structured data table supporting local sorting, paging, search, and data binding.",
		SupportedEvents: []string{"sortTable", "setPage"},
		AllowedChildren: []string{},
		PropsSchema: map[string]any{
			"title":      map[string]any{"type": "string"},
			"data":       map[string]any{"description": "Data reference object or inline row array"},
			"columns":    map[string]any{"type": "array", "description": "Column definitions with field, label, kind"},
			"sortable":   map[string]any{"type": "boolean"},
			"pageSize":   map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
		},
		Example: map[string]any{
			"type":  "table",
			"id":    "resultsTable",
			"title": "Per-Executor Performance",
			"data": map[string]any{
				"source":  "observations",
				"pointer": "/executors",
			},
			"columns": []any{
				map[string]any{"field": "executor", "label": "Executor", "kind": "text"},
				map[string]any{"field": "runs", "label": "Runs", "kind": "integer"},
				map[string]any{"field": "successRate", "label": "Success Rate", "kind": "percent"},
			},
			"sortable": true,
			"pageSize": 25,
		},
	},
	{
		Type:            "json",
		Version:         1,
		Category:        "Data",
		Description:     "Safe, interactive structured JSON inspector for raw payloads or references.",
		SupportedEvents: []string{},
		AllowedChildren: []string{},
		PropsSchema: map[string]any{
			"title":     map[string]any{"type": "string"},
			"data":      map[string]any{"description": "Data reference object or inline value"},
			"collapsed": map[string]any{"type": "boolean"},
		},
		Example: map[string]any{
			"type":  "json",
			"id":    "rawInspection",
			"title": "Configuration Payload",
			"data": map[string]any{
				"source":  "observations",
				"pointer": "/config",
			},
		},
	},

	// Charts
	{
		Type:            "barChart",
		Version:         1,
		Category:        "Charts",
		Description:     "Categorical bar chart with bounded scaling and format identifiers.",
		SupportedEvents: []string{"selectChartPoint"},
		AllowedChildren: []string{},
		PropsSchema: map[string]any{
			"title":         map[string]any{"type": "string"},
			"data":          map[string]any{"description": "Data reference object or inline row array"},
			"categoryField": map[string]any{"type": "string"},
			"valueField":    map[string]any{"type": "string"},
			"format":        map[string]any{"type": "string", "enum": []string{"number", "percent", "bytes", "duration"}},
			"selectedKey":   map[string]any{"type": "string"},
		},
		Example: map[string]any{
			"type":          "barChart",
			"id":            "dailyVolume",
			"title":         "Runs by Day",
			"data":          map[string]any{"source": "observations", "pointer": "/daily"},
			"categoryField": "day",
			"valueField":    "runs",
			"format":        "number",
		},
	},
	{
		Type:            "sparkline",
		Version:         1,
		Category:        "Charts",
		Description:     "Compact trend visualization for series data.",
		SupportedEvents: []string{},
		AllowedChildren: []string{},
		PropsSchema: map[string]any{
			"title":      map[string]any{"type": "string"},
			"data":       map[string]any{"description": "Data reference or array of numbers/objects"},
			"valueField": map[string]any{"type": "string"},
			"labelField": map[string]any{"type": "string"},
		},
		Example: map[string]any{
			"type":       "sparkline",
			"id":         "trend",
			"title":      "Hourly Throughput",
			"data":       []any{12, 19, 23, 17, 28, 34, 42},
			"valueField": "value",
		},
	},

	// Controls
	{
		Type:            "select",
		Version:         1,
		Category:        "Controls",
		Description:     "Dropdown selector updating a local state variable.",
		SupportedEvents: []string{"setState"},
		AllowedChildren: []string{},
		PropsSchema: map[string]any{
			"label":   map[string]any{"type": "string"},
			"initial": map[string]any{"type": []string{"string", "number"}},
			"options": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"value": map[string]any{"type": []string{"string", "number"}},
						"label": map[string]any{"type": "string"},
					},
				},
			},
		},
		Example: map[string]any{
			"type":    "select",
			"id":      "periodFilter",
			"label":   "Time Window",
			"initial": "7d",
			"options": []any{
				map[string]any{"value": "7d", "label": "Last 7 Days"},
				map[string]any{"value": "30d", "label": "Last 30 Days"},
			},
		},
	},
	{
		Type:            "toggle",
		Version:         1,
		Category:        "Controls",
		Description:     "Boolean toggle switch updating a local state variable.",
		SupportedEvents: []string{"setState"},
		AllowedChildren: []string{},
		PropsSchema: map[string]any{
			"label":   map[string]any{"type": "string"},
			"initial": map[string]any{"type": "boolean"},
		},
		Example: map[string]any{
			"type":    "toggle",
			"id":      "showFailuresOnly",
			"label":   "Show Failures Only",
			"initial": false,
		},
	},
	{
		Type:            "search",
		Version:         1,
		Category:        "Controls",
		Description:     "Text input updating a filter state variable for tables or data views.",
		SupportedEvents: []string{"setState"},
		AllowedChildren: []string{},
		PropsSchema: map[string]any{
			"label":       map[string]any{"type": "string"},
			"placeholder": map[string]any{"type": "string"},
			"initial":     map[string]any{"type": "string"},
		},
		Example: map[string]any{
			"type":        "search",
			"id":          "searchFilter",
			"label":       "Filter",
			"placeholder": "Search rows...",
			"initial":     "",
		},
	},

	// File references
	{
		Type:            "artifactLink",
		Version:         1,
		Category:        "File references",
		Description:     "Authorized reference link to a sibling output port produced by the same activation.",
		SupportedEvents: []string{},
		AllowedChildren: []string{},
		PropsSchema: map[string]any{
			"port":        map[string]any{"type": "string", "description": "Authorized sibling output port name"},
			"label":       map[string]any{"type": "string"},
			"description": map[string]any{"type": "string"},
		},
		Example: map[string]any{
			"type":        "artifactLink",
			"id":          "linkRawMetrics",
			"port":        "metrics",
			"label":       "Download Raw Metrics",
			"description": "Full JSON observations file from this execution.",
		},
	},
}

// Catalog returns all registered Canvas v1 component specifications.
func Catalog() []ComponentSpec {
	res := make([]ComponentSpec, len(catalog))
	copy(res, catalog)
	return res
}

// GetComponent finds a component specification by its type name.
func GetComponent(typeName string) (*ComponentSpec, error) {
	for i := range catalog {
		if catalog[i].Type == typeName {
			return &catalog[i], nil
		}
	}
	return nil, fmt.Errorf("unknown canvas component type %q", typeName)
}
