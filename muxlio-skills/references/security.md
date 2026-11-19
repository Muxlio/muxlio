# Execution permissions and recovery

Human-oriented setup: [What Muxlio is](https://muxlio.github.io/muxlio/#what-is-muxlio), [Setup defaults](https://muxlio.github.io/muxlio/service/#setup-defaults).

A skill invocation does not grant execution permissions. Keep workflows within
the user's authorized workspace, tools, and intended external effects.
`muxlio init --allow-command` explicitly authorizes the command executor; use it
only when requested. Compilation of TypeScript executes arbitrary local source code with host
filesystem and network access. Use `check_plan` or JSON files for untrusted
submissions. MCP source calls require `trustedSource: true`. Worker sandbox policies do not isolate compilation.

Keep tokens in protected runtime configuration or token files. Do not put secrets
in plans, prompts, client settings, process arguments, logs, or artifacts.
A workspace is an execution target, not a filesystem sandbox; select the runtime's
isolation policy when the task requires isolation.

Bound retries and loops. Before retrying an operation with uncertain external
effects, inspect its outputs and run state to avoid duplicate actions. Preserve
completed work and stop for user input when additional authorization is required.
