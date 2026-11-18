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
