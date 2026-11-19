# Use Muxlio from your assistant

--8<-- "snippets/root-context.md"

MCP connects your assistant to the coordinator so it can author plans, validate
them, start runs, and inspect results while execution continues independently.
Use it for multi-step, durable work—not for one-off chat tasks unless you
explicitly want orchestration.

## Connect once

First follow [Start Muxlio](service.md) and [Setup defaults](service.md#setup-defaults):
run `muxlio service start`, then use stdio `muxlio-mcp` (normal for assistants)
or optional Streamable HTTP on port **7602**.

Verify stdio registration:

```sh
muxlio integrations doctor --client kiro --scope project
```

Use `integrations add` only when [Setup defaults](service.md#setup-defaults)
says you need it:

```sh
muxlio integrations add --client kiro --scope project
```

Registration is noninteractive and uses the installed absolute `muxlio-mcp`
executable over stdio by default. Use `--binary /absolute/path/to/muxlio-mcp`
if it is not beside `muxlio` or on PATH. The bridge reads coordinator credentials
from the protected Muxlio config; tokens are never copied into client settings.
Use `--data-dir` for a nondefault Muxlio runtime and `--server NAME` to select a
saved coordinator profile.

Every integrations command requires `--scope project` or `--scope user`.
`--workspace /absolute/project` selects the project (default: current directory).
Only the chosen client and scope are modified:

| Client | Project config | User config |
| --- | --- | --- |
| Claude Code | `.mcp.json` | `~/.claude.json` |
| Codex CLI / IDE | `.codex/config.toml` | `$CODEX_HOME/config.toml` or `~/.codex/config.toml` |
| Cursor | `.cursor/mcp.json` | `~/.cursor/mcp.json` |
| Kiro | `.kiro/settings/mcp.json` | `~/.kiro/settings/mcp.json` |

Muxlio merges the named `muxlio` server, preserving unrelated settings. Repeating
an identical add leaves the file unchanged. A conflicting custom or edited entry
is rejected with instructions; it is never overwritten. Config symlinks are
rejected. JSON and TOML may be reformatted on a change (including TOML comments).
A hash receipt beside the config tracks ownership without storing credentials.
Keep that receipt to allow safe repair and removal.

```sh
muxlio integrations list --scope project
muxlio integrations doctor --client kiro --scope project --config-only
muxlio integrations repair --client kiro --scope project --binary /new/path/muxlio-mcp
muxlio integrations remove --client kiro --scope project
```

`repair` can recreate a missing registration or update an unchanged managed
entry. Custom or edited entries require manual review first. `remove` deletes
only an unchanged managed entry. Restart the client after changes.

Doctor starts the actual client's noninteractive CLI and requests `server_health`
and `get_workers`, requiring structured successful tool results. It can consume
model quota and requires client login, project trust and MCP approvals. It does
not bypass workspace trust. If the CLI is absent, unsupported by its installed
version, or cannot confirm the calls, doctor fails with recovery instructions;
open the IDE and ask it to invoke both tools. `--config-only` checks registration
and executable/endpoint validity without claiming a live connection. A user
registration cannot be verified in a workspace with a project `muxlio` override.
Claude's private local-scope `muxlio` entry also takes precedence over both scopes;
doctor asks you to review that entry before verifying a different registration.
Client output and configuration contents are withheld from diagnostics.

### Remote HTTP

Use the service's Streamable HTTP endpoint and an environment variable containing
its bearer token. Export the variable securely in the environment that launches
the client; Muxlio stores only its name, using each client's interpolation syntax.
Kiro IDE may require approval of that environment variable. Remote endpoints
require HTTPS; HTTP is accepted only for loopback addresses.

```sh
muxlio integrations add --client codex --scope user --transport http \
  --url https://muxlio.example.com/mcp --bearer-env MUXLIO_MCP_TOKEN
```

For saved remote endpoints, add an `mcpProfiles` entry to your protected Muxlio
`config.json`, then use `--transport http --profile office`:

```json
{"mcpProfiles":{"office":{"url":"https://muxlio.example.com/mcp","bearerEnv":"MUXLIO_MCP_TOKEN"}}}
```

### Install and invoke the Muxlio skill

Run `muxlio init --client NAME --scope project --workspace /absolute/project`
to install the complete skill bundle in a target project, including a repository
unrelated to Muxlio. Use `--scope user` for discovery across projects. Skill
locations are separate from MCP configuration:

| Client | Project skill | User skill |
| --- | --- | --- |
| Claude Code | `.claude/skills/muxlio/SKILL.md` | `~/.claude/skills/muxlio/SKILL.md` |
| Codex | `.agents/skills/muxlio/SKILL.md` | `~/.agents/skills/muxlio/SKILL.md` |
| Kiro | `.kiro/skills/muxlio/SKILL.md` | `~/.kiro/skills/muxlio/SKILL.md` |
| Cursor 2.4+ | `.cursor/skills/muxlio/SKILL.md` | `~/.cursor/skills/muxlio/SKILL.md` |

Codex's skill location is independent of `CODEX_HOME`. Cursor setup checks
`cursor --version`. Older or unrecognized versions receive a manual project rule
at `.cursor/rules/muxlio.mdc`, with its bundle under `.cursor/muxlio`. Global
filesystem rules are unsupported: for user scope, make Cursor 2.4+ available on
PATH or use project scope. An existing project rule keeps its complete activation
frontmatter, including descriptions, globs and `alwaysApply`, rather than being
converted to a skill with different activation behavior.

Setup archives legacy `.skills/muxlio` and Codex `.codex/skills/muxlio` directories
(also `$CODEX_HOME/skills/muxlio` for user scope) under
`.muxlio-skill-backups/legacy-*/muxlio` in the selected scope. Relative resources
and user edits remain together, outside discovery. Changed destination files,
including customized Cursor rule bodies, are saved beside the file with a
`.muxlio-backup-<content hash>` suffix before the canonical content is installed.
Unrelated files are retained. Review and reapply custom guidance as needed.
Init reports these paths in text or JSON `skillsPreserved`; repeated setup leaves
identical files alone. Write failures and invalid skill metadata fail setup.

Restart the client in the target project. Invoke `/muxlio` in Claude Code or
Cursor's skill menu, `$muxlio` in Codex, or explicitly ask Kiro to use the Muxlio
skill (custom Kiro agents must include `skill://.kiro/skills/**/SKILL.md` in their
resources). For Cursor's manual rule, attach `@muxlio`.
Ask it to use Muxlio to draft a workflow summarizing supplied documents, without
starting it. Confirm the skill loads and its authoring reference is readable.
Then start a fresh chat and ask a trivial unrelated question, such as "What is
2 + 2?"; it should answer directly without invoking Muxlio. The shared skill
also covers when durable, multi-step, scheduled, independently verifiable, or
multi-executor work benefits from orchestration. Small interactive tasks stay
in the client unless Muxlio is explicitly requested. Every client uses the same
decision guide, preflight, approval, and completion evidence rules; native skill
syntax does not select an execution adapter. Existing custom rule activation
settings remain the user's choice.

The [delegation acceptance harness](https://github.com/Muxlio/muxlio/blob/main/muxlio-testing/test/delegation-acceptance.md)
compares the same prompts and workflow semantics across authoring clients,
including local-only work, plan approval, preflight blockers, and run evidence.

Skill discovery contracts: [Claude Code](https://code.claude.com/docs/en/skills),
[Codex](https://developers.openai.com/codex/skills/),
[Kiro](https://kiro.dev/docs/skills/),
[Cursor skills](https://cursor.com/docs/skills), and
[Cursor rule activation](https://cursor.com/docs/rules).

Client config contracts: [Claude Code](https://code.claude.com/docs/en/mcp),
[Codex](https://developers.openai.com/codex/mcp/),
[Cursor](https://cursor.com/docs/context/mcp), and
[Kiro](https://kiro.dev/docs/mcp/configuration/).

## Ask for a workflow

To start from a reusable shape, ask the assistant to call
`list_workflow_templates`, inspect a template's schemas, and call
`instantiate_workflow_template` with your chosen executors. The
[workflow template catalog](workflow-templates.md) provides five
generic shapes and a non-code example. Instantiation returns an ordinary plan
and digest for inspection, validation and submission with separate run inputs.

Describe the inputs, the steps to run, the checks they must pass, and the output
you need. For example:

> Build a Muxlio workflow for implementing a list of features in this project.
> For each feature, implement it, review the change, and run this project's build
> and tests. If checks fail, fix the issue and retry up to a limit. Commit locally
> only after checks pass. Show me the workflow before starting it.

The assistant can construct Plan IR directly or instantiate a template. For
one-off work, it validates the plan with `check_plan`, runs `preflight_run` with
the plan, input, and target workspace, then sends those same values to `start_run`
after a feasible preflight. Both single-step and multi-step plans use this flow.
Developers can also author workflow source and use `build` and `test` for
reviewed, trusted definitions. Source compilation executes arbitrary code with
host filesystem and network access. It requires `trustedSource: true` on the MCP call.
Prefer `check_plan` or JSON files for untrusted submissions; see the
[compilation security guide](architecture/execution-security.md#workflow-compilation).

## Follow the work

Ask your assistant to check the run, read logs and reports, or explain a failure.
You can also use the [web UI](ui-operations.md). Closing the client leaves the run
on the coordinator. Your execution machine still needs to stay running.

Use `get_run_summary` with the saved `runId` from any MCP client connected to the
same coordinator to retrieve the current outcome and links later. `get_run`,
`get_input_requests`, `get_attempts`, `get_logs`, and artifact tools provide the
full retained details, subject to the client's normal permissions. Task handles
can also be polled while retained; a live original conversation is unnecessary.

Operators can optionally configure [signed run notifications](architecture/developer-reference.md#outbound-lifecycle-subscriptions)
through `POST /v1/subscriptions`, using `payloadMode: "notification"` and event
filters for completion, failure, input required, and reconciliation required.
Receivers get metadata and visible run/status links, with durable event IDs and
bounded retries. No prompt, transcript, answer, or failure text is included in
this mode. The receiver decides how to surface the alert; a client integration
can opt in if its host supports notifications. Muxlio cannot generally resume a
disconnected conversation or reopen a closed chat. Notifications are optional
and do not replace later status retrieval.

For client configuration examples, tool arguments, authentication, and protocol
behavior, see the [MCP reference](architecture/mcp-reference.md).

## Start a one-off workflow inline

If the run needs an authorized workspace, call `list_workspaces` with an
administrator credential or a `workspaces:read` grant. Use the returned canonical
`workspaceId` in both `preflight_run` and `start_run`. When the workspace
has a configured digest, pin it as `workspaceDigest` in the plan. See the
[tool reference](architecture/mcp-reference.md#list_workspaces).

The following single-step plan echoes the run input. Call `check_plan` with
the `plan` field. Pass the complete arguments below to `preflight_run`, then
to `start_run` after a feasible result. Add the same `workspaceId` to both
calls when targeting a specific workspace.

```json
{
  "plan": {
    "irVersion": "1.0",
    "name": "echo",
    "root": "root",
    "subgraphs": {
      "root": {
        "nodes": [{"id": "echo", "kind": "project", "inputs": {"value": {"expr": {"op": "input"}}}}],
        "output": {"result": {"ref": {"node": "echo", "port": "value"}}}
      }
    }
  },
  "input": {"message": "Hello"}
}
```

The coordinator retains the exact plan used for the run without adding a
workflow registry entry. The [template walkthrough](workflow-templates.md#summarize-and-verify-a-museum-guide)
shows the same inline flow for a multi-step plan.

## Save and start a registered workflow

Register when the user explicitly asks to save or publish a workflow, or when
a concrete use case needs a reusable version or a registered pin for a
schedule or trigger. See [schedules](architecture/service-reference.md#run-schedules)
and [triggers](architecture/developer-reference.md#inbound-webhook-triggers).
Use the named workflow tools below when publishing under a stable name.

Call `register_workflow_version` once with `{"plan": <compiled Plan IR>}`.
The coordinator validates and stores an immutable definition and returns its
`digest`. Any authenticated client of that coordinator can reuse it:

```json
{
  "planDigest": "sha256:<digest returned by registration>",
  "workspaceId": "project",
  "input": {"request": "Generate the weekly report"},
  "idempotencyKey": "weekly-report-2026-11-13"
}
```

Pass these arguments to `start_run`. Supply exactly one of `plan` or
`planDigest`. Inputs must match the registered plan's `inputSchema`.
The coordinator loads the definition, validates input, authorizes the target
workspace, and pins the registered bytes and digest
without rewriting them. Workspace aliases resolve to a canonical run workspace,
stored separately from the definition. A workspace fixed in the plan cannot be
overridden with a different workspace.

Reuse the same idempotency key on retries. The same authenticated caller,
definition, exact input bytes, and canonical workspace return the original run;
changed inputs or a changed definition require a new key. Omit `workspaceId`
to use the plan's workspace or the coordinator's configured default.

The HTTP equivalent is `POST /v1/runs` with the same body fields except that
`idempotencyKey` is sent as the `Idempotency-Key` header. Register through
`POST /v1/workflow-versions` with the Plan IR itself as the body.
`list_workflow_versions` lists registered digests and their retirement state.
Unknown digests return 404. Operators can retire a definition with authenticated
`DELETE /v1/workflow-versions/<digest>`; future reference submissions return 410
(`MUXLIO_WORKFLOW_VERSION_RETIRED`). Retirement persists across restart and
does not change existing runs. Uploading identical content does not reactivate a
retired version. Change the definition and register its new digest.

## Publish a named workflow

Use the coordinator's operator credential to create, inspect, and update named
workflows. Supply a stable `workflowId` to `create_workflow` along with the
compiled `plan`. The plan's name is display text and can change across versions.

| MCP tool | HTTP endpoint | Arguments |
| --- | --- | --- |
| `create_workflow` | `POST /v1/workflows` | `workflowId`, `plan` |
| `get_workflow` | `GET /v1/workflows/{workflowId}` | `workflowId` in the HTTP path |
| `update_workflow` | `POST /v1/workflows/{workflowId}` | `workflowId` in the HTTP path, `expectedCurrentRevision`, and either `plan` or `planDigest` |

Each response includes `currentRevision`, `currentDigest`, and the history
with each version's digest, display name, and publication time. The current
version appears first, followed by the other versions in reverse publication
order. To publish a new version, pass a compiled `plan` and the
`currentRevision` you observed as `expectedCurrentRevision`. Concurrent
changes return HTTP 409 for a competing update. Repeating a publication keeps
the original history entry and timestamp. To switch back, pass the prior
version's `planDigest` with the same revision guard.

Pass `currentDigest` or any eligible history digest as `planDigest` to
`start_run`. Workspace selection and authorization still apply when submitting
the run. Every run keeps its exact plan bytes, and schedules and triggers
continue to use their configured digest. Choose a new digest explicitly when
configuring automation. A digest can be retired once no named workflow uses it
as current. Retired versions remain visible in history and are ineligible for
selection.
