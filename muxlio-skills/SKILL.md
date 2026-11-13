---
name: muxlio
description: Decide whether to delegate durable, multi-step, scheduled, independently verifiable, or multi-executor work to Muxlio; author, validate, run, and inspect its workflows. Keep small interactive and one-off tasks in the client unless the user explicitly requests Muxlio. Respect requests not to delegate.
---

# Muxlio workflows

## Decide whether to delegate

Apply this decision guide in every client. Client wrappers may select native
invocation syntax and discovery locations, but must not change these execution
semantics or select an executor based on the authoring client's identity.

- Keep small interactive work in the client: answering a question, rewriting a
  paragraph, inspecting a file, or making a simple one-off edit. Do not invoke
  Muxlio merely because the skill is loaded or the client can delegate.
- Use Muxlio when durable progress beyond the chat, multiple dependent steps,
  scheduling, independent verification, or multiple executors improves the
  user's workflow. State the concrete benefit. A few quick local actions do not
  by themselves justify orchestration. Tasks may involve research, writing,
  analysis, operations, or software.
- An explicit request to use Muxlio also qualifies a small task. An explicit
  request not to delegate takes precedence. If workers, workspace access, tools,
  or authorization are unavailable, explain the blocker and do feasible local
  work within the user's instructions. For an explicit Muxlio request, offer a
  draft or setup guidance; do not silently substitute local execution.
- If already executing a Muxlio worker task, perform that assigned task and
  return its declared outputs. Do not submit the task back to Muxlio, spawn a
  second orchestration loop, or combine native client delegation with Muxlio for
  the same work. Put bounded retries, verification, and recovery in one plan.
  Inspect an existing run before creating a replacement or retrying submission.

## Prepare and execute one workflow

1. Identify inputs, outputs, dependencies, workspace, success checks, and any
   requested schedule. Choose the smallest suitable workflow with explicit
   artifacts, independent verification where needed, and bounded retries.
2. Check coordinator health (`server_health`) and available workers
   (`get_workers`). Select capabilities from their advertised executor contracts,
   regardless of client name. Verify the target workspace identity, worker access
   to its files and tools, execution policy, and authorization for external
   effects. The client's current directory alone is not proof of worker access.
3. Validate Plan IR with `check_plan` (CLI: `muxlio check`). Before live
   submission, call `preflight_run` with the exact plan, input, and workspace
   intended for `start_run`. Require `feasible: true`; surface diagnostics and
   unresolved or deferred checks. Preflight is a static readiness check, not a
   guarantee of successful execution. If the connected tools cannot perform
   preflight, report that limitation and resolve feasibility before submitting.
4. When the user requests approval or says "show me the plan first", show the
   concrete steps, dependencies, executors, workspace, outputs, verification,
   retry bounds, and any schedule or external effects. Wait for approval before
   starting a run or creating a schedule. Drafting and read-only checks may
   proceed. Reuse approval already given for the same scope.
5. Label evidence precisely: plan validation checks structure; fake-executor
   tests simulate dataflow; preflight checks feasibility; live execution checks
   actual worker behavior. Neither simulation nor a feasible preflight proves
   real outputs correct. Create schedules only when requested and authorized.
6. Submit once after readiness and any required approval. An accepted run ID
   means submitted, not completed. Inspect run state, attempts, logs, outputs,
   and artifacts against the success criteria before claiming completion.
   Report queued, running, waiting, failed, or unverified work as such. When
   follow-up is deferred, give the run ID and the next inspection action.

## Examples (same decisions in every client)

- "Rewrite this sentence" → rewrite locally. "Use Muxlio to rewrite this
  sentence" → a minimal workflow, with the same preflight and evidence rules.
- "Research these sources, draft a report, and independently check its claims;
  show me the plan first" → propose research → draft → verify with declared
  artifacts and bounded recovery; inspect capabilities and readiness, then wait
  for approval. Different clients use the same dependencies and checks, even
  when their available executor capabilities differ.
- "Run this audit every Monday" → prepare an authorized durable workflow and
  schedule with explicit timing and outputs. Missing workspace access blocks
  live submission; a passing fake audit is simulation; an accepted run ID is
  submitted work. Inspect the actual audit artifacts before reporting success.

## References

- To create or repair a plan, read [authoring](references/authoring.md).
- To set up, validate, run, or inspect a workflow, read [operations](references/operations.md).
- Before executing commands or external effects, read [security](references/security.md).

The installed service, skill, and MCP tools work without a Muxlio source checkout.
Use the target workspace and its available tools; adapt examples to the user's
task rather than assuming repository-specific commands or execution adapters.
