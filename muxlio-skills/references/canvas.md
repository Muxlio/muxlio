# Muxlio Canvas

Human-oriented setup: [What Muxlio is](https://muxlio.github.io/muxlio/#what-is-muxlio), [Setup defaults](https://muxlio.github.io/muxlio/service/#setup-defaults).

Canvas lets any executor publish an **immutable, declarative UI document** as an ordinary workflow artifact. The Muxlio web UI renders it using an allowlisted component registry. Agents control content, layout, data selection, and local interactions; they never provide HTML, CSS, JavaScript, or operational actions.

## When to use Canvas vs Markdown

- Use **Canvas** when the output benefits from structured layout (metric cards, sortable tables, bar charts, tabbed sections, filters), or when sibling data files should be bound to interactive components without a model call.
- Use **Markdown** when the output is a continuous prose document, a report with inline tables, or a quick summary. Markdown artifacts are always available in the inspector.
- Both can be produced in the same workflow. Canvas is a complementary surface, not a replacement.

## Workflow output declaration

Declare the Canvas file and any data files as named output ports. Use MIME `application/vnd.muxlio.canvas+json` for the Canvas document:

```json
{
  "outputs": {
    "canvas": { "path": "report.canvas.json", "mime": "application/vnd.muxlio.canvas+json" },
    "metrics": { "path": "metrics.json", "mime": "application/json" }
  }
}
```

After the run completes, the UI shows an **Open Canvas** link beside the artifact.

## Authoring a Canvas document

1. **Discover available components** — call `list_canvas_components` for a summary of all types and categories, then `get_canvas_component` with a specific `type` to get its full props schema and a working example.
2. **Write the document** — produce valid JSON matching the Canvas v1 schema: `version: 1`, `title`, optional `sources`, optional `controls`, and a `content` tree of approved component nodes. Each node needs a unique `id` and a `type` from the approved vocabulary.
3. **Validate before publishing** — call `validate_canvas` with the document content or a file path. Fix any reported errors. Validation is offline and does not require a live coordinator.
4. **Write the file** — save as `report.canvas.json` (or the path declared in the output port) in the authorized execution workspace.

## Approved component types (v1)

| Category | Types |
|---|---|
| Layout | `stack`, `row`, `grid`, `section`, `tabs`, `divider` |
| Reading | `heading`, `text`, `markdown`, `code`, `callout` |
| Facts | `metric`, `keyValue`, `status`, `summary` |
| Data | `table`, `json` |
| Charts | `barChart`, `sparkline` |
| Controls | `select`, `toggle`, `search` |
| File references | `artifactLink` |

Call `get_canvas_component` for the exact props schema and required fields for any type. Unknown types fail validation.

## Data binding

A `sources` map names data files from the same activation. Reference them in `data` props with a `source` name, an optional JSON pointer, and an optional filter:

```json
{
  "sources": { "metrics": { "artifactPort": "metrics" } },
  "content": {
    "type": "table",
    "id": "results",
    "data": { "source": "metrics", "pointer": "/executors" },
    "columns": [
      { "field": "executor", "label": "Executor", "kind": "text" },
      { "field": "successRate", "label": "Success rate", "kind": "percent" }
    ],
    "sortable": true
  }
}
```

Small values can be inlined directly under `{ "value": ... }` (limit: 256 KiB). Referenced JSON files must be on the same activation and are fetched through the authenticated artifact endpoint (limit: 5 MiB). Non-JSON ports are not resolved.

## Controls and local state

Controls (`select`, `toggle`, `search`) update browser-local state. Use `equalsState` in a data filter to bind a table or chart to a control value. All interactions are local — no model call is made when a user changes a filter.

```json
{
  "type": "select",
  "id": "period",
  "label": "Period",
  "initial": "7d",
  "options": [
    { "value": "7d", "label": "Last 7 days" },
    { "value": "30d", "label": "Last 30 days" }
  ]
}
```

Then reference it in a data filter:

```json
{ "source": "metrics", "pointer": "/daily", "filter": { "field": "period", "equalsState": "period" } }
```

## Safety limits

| Limit | Value |
|---|---|
| Canvas document | 1 MiB |
| Inline source data | 256 KiB |
| Referenced JSON data | 5 MiB |
| Component count | 500 |
| Tree depth | 16 |
| Chart points | 2 000 |
| Table rows per page | 100 |

Documents that exceed these limits fail validation or show a bounded placeholder. The validator reports the first violation with its field path.

## Example document

```json
{
  "version": 1,
  "title": "Execution Quality Review",
  "sources": {
    "observations": { "artifactPort": "metrics" }
  },
  "content": {
    "type": "stack",
    "id": "root",
    "gap": "comfortable",
    "children": [
      { "type": "heading", "id": "h1", "level": 1, "text": "Execution Quality Review" },
      {
        "type": "grid",
        "id": "kpis",
        "columns": 3,
        "children": [
          { "type": "metric", "id": "runs",     "label": "Total runs",    "value": 248 },
          { "type": "metric", "id": "success",  "label": "Success rate",  "value": "96.4%" },
          { "type": "metric", "id": "duration", "label": "Avg duration",  "value": "42 s" }
        ]
      },
      {
        "type": "select", "id": "period", "label": "Period", "initial": "7d",
        "options": [
          { "value": "7d",  "label": "Last 7 days" },
          { "value": "30d", "label": "Last 30 days" }
        ]
      },
      {
        "type": "barChart", "id": "volume", "title": "Runs by day",
        "data": { "source": "observations", "pointer": "/daily",
                  "filter": { "field": "period", "equalsState": "period" } },
        "categoryField": "day", "valueField": "runs"
      },
      {
        "type": "table", "id": "details", "title": "Per-executor performance",
        "data": { "source": "observations", "pointer": "/executors" },
        "columns": [
          { "field": "executor",    "label": "Executor",      "kind": "text" },
          { "field": "runs",        "label": "Runs",          "kind": "integer" },
          { "field": "successRate", "label": "Success rate",  "kind": "percent" }
        ],
        "sortable": true, "pageSize": 25
      },
      {
        "type": "markdown", "id": "notes",
        "text": "## Notes\nFigures are based on the workflow-generated data file."
      }
    ]
  }
}
```
