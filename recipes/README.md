# Workflow recipes

For common shapes, start with the built-in [workflow template catalog](../muxlio-docs/workflow-templates.md).
Any MCP client can call `list_workflow_templates`, inspect a template with
`get_workflow_template`, and compile it with `instantiate_workflow_template`.
The five versioned shapes cover sequential verification, bounded parallel maps,
review/repair loops, external approval, and command pipelines. They work with
non-code tasks and client-selected executor capabilities.

Template parameters describe the plan; run inputs supply the data. Instantiation
returns an editable ordinary Plan IR definition and digest without running or
registering it. For one-off work, validate with `check_plan`, preflight the plan
and run input with `preflight_run`, and pass both to `start_run` after a feasible
preflight. Use `register_workflow_version` to save a compiled plan for reuse or
to pin a schedule or trigger. A template version identifies an authoring recipe.
An immutable workflow version identifies a compiled plan.

The TypeScript recipes in this directory show more specialized compositions
using `@muxlio/sdk`. Use them when the built-in shapes do not fit, with the same
Plan IR execution and inspection tools.

See [research](research/README.md) for iterative evidence gathering with a final
Markdown report and Canvas dashboard.

## Append features to a running batch

The [Docker Sandbox walkthrough](../muxlio-docs/docker-sandbox.md) shows how to
run the fixed or live feature recipe on a sandbox worker with Claude Code,
Codex, Cursor, and `command` in one private Git clone.

Submit `recipes/implement-features/live.ts` to the coordinator using the same input
and workspace as the fixed batch in `recipes/implement-features/workflow.ts`. Keep
the run ID returned by submission. The live recipe accepts up to 20 features
total, including those in the submitted input. Each feature runs to completion
in acceptance order on the shared checkout.

Call these MCP tools with the original run ID:

- `append_run_collection_item` with
  `{"runId":"RUN_ID","collection":"features","item":{"id":"next","feature":"Describe the change"}}`.
  The item supports the same fields as an initial feature, including optional
  `context`. Retry with the same item and ID to recover a lost response.
- `get_run_collection` with `{"runId":"RUN_ID","collection":"features"}` to
  inspect accepted features, whether the collection is open, and any idle deadline.
- `close_run_collection` with `{"runId":"RUN_ID","collection":"features"}`
  when finished adding features. Closing lets all accepted features drain.
  If left open, the collection closes after 15 minutes without pending work.

Use a run-scoped `inputs` grant to append or close. The fixed recipe completes
when its submitted feature list is exhausted.
