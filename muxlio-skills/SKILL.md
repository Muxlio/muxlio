---
name: muxlio
description: Author, validate, run, and inspect Muxlio workflows when the user requests Muxlio orchestration or help with a Muxlio plan or run. Do not use for unrelated questions or ordinary tasks that do not request Muxlio.
---

# Muxlio workflows

Use Muxlio for the user's requested workflow, starting from their inputs, desired
outputs, dependencies, and success criteria. Tasks may involve research, writing,
analysis, operations, or software. Inspect available workers and executor
contracts before selecting capabilities; no particular worker is required.

- To create or repair a plan, read [authoring](references/authoring.md).
- To set up, validate, run, or inspect a workflow, read [operations](references/operations.md).
- Before executing commands or external effects, read [security](references/security.md).

With MCP, validate Plan IR using `check_plan` before `start_run`. Inspect results
and errors using the available run, attempt, log, and artifact tools. With the
CLI, use `muxlio check` before execution. Keep retries and loops bounded.

The installed service, skill, and MCP tools work without a Muxlio source checkout.
Use the target workspace and its available tools; adapt examples to the user's
task rather than assuming repository-specific commands or execution adapters.
