# Start Muxlio

--8<-- "snippets/root-context.md"

Muxlio is a **durable workflow coordinator** for multi-step work across AI agents
and command-line tools. You author an immutable **plan**; **`service start`**
runs the **coordinator**, a local **worker**, the web UI, and MCP on the machine
that executes your workflows. Each **run** keeps state after your chat
disconnects.

Run Muxlio on the machine that will do the work. That can be your workstation,
another computer, or a server. Keep that machine running while workflows execute.
Use it when steps need verification, retries, or hand-offs between agents and
shell commands—not for one-off questions you can answer in the client. Plan/run vocabulary is on the [documentation home](index.md#what-is-muxlio).

## Setup defaults

--8<-- "snippets/setup-defaults.md"

## Install

With [Homebrew](https://brew.sh) on macOS or Linux:

```sh
brew tap Muxlio/muxlio https://github.com/Muxlio/muxlio
brew install --HEAD Muxlio/muxlio/muxlio
muxlio version
```

Homebrew builds the current main branch and installs the CLI, MCP server,
adapters, and UI. You do not need to clone Muxlio. Install the agent CLIs your
workflow uses and sign in to them on the worker machine. Install any tools your
workflow commands need as well.

## Set up your workspace

From the project or workspace where you want work to happen:

```sh
muxlio init --client kiro --scope project
muxlio service start
```

Choose `--client` for the assistant you use (`kiro`, `codex`, `claude`, `cursor`,
or `antigravity` for instructions only). Client registration rules and default
ports are in [Setup defaults](#setup-defaults). Use `--scope user` for user-wide
discovery. `--agent` is a compatible alias for `--client`.
Fresh configs enable all bundled executors, including `command` for build checks
and Git. To restrict which capabilities the local worker advertises, edit
`capabilities` in `config.json`. Use the `executors` map for per-executor
operator policies (`allow`, timeouts, environment, and authorization). Repeated
initialization preserves explicit policies and disabled executors. Use
`--allow-command` to re-enable a previously disabled `command` executor.

`init` saves your workspace and prepares runtime directories and access
credentials. `service start` starts the coordinator, local worker, bundled UI,
and MCP HTTP listener as described under [Setup defaults](#setup-defaults).

## Select an authorized project per run

Use `muxlio workspace` to manage the operator-owned `workspaces` map in the
local runtime config. It selects `MUXLIO_CONFIG` when set, then
`config.json` in the directory selected by `--data-dir`, then `config.json` in
`MUXLIO_HOME` or
`~/.muxlio`. Place `--data-dir` before `workspace` or after the workspace
subcommand.

For a coordinator with its built-in worker, use the worker ID `local`.
Provision the directory first, then register it and restart the service.

```sh
muxlio workspace add documents /srv/muxlio/documents --worker-id local --alias docs
muxlio workspace list
muxlio workspace show docs --json
muxlio service restart

# After retiring this binding
muxlio workspace remove documents
muxlio service restart
```

For a separate worker, use the exact identity passed to `worker start`.
Run the same add command on each host, selecting that host's config file.
The path is absolute on the worker. The owning worker resolves and validates
it during startup.

```sh
# On the coordinator host
MUXLIO_CONFIG=/etc/muxlio/coordinator.json muxlio workspace add dataset /srv/data/current --worker-id data-node --alias data

# On the worker host
MUXLIO_CONFIG=/etc/muxlio/worker.json muxlio workspace add dataset /srv/data/current --worker-id data-node --alias data
```

For removal, run `workspace remove dataset` against both files. Restart the
coordinator and owning worker after updating their configs. Each command
operates on its selected local file. `list --json` returns an array of bindings
and `show <id-or-alias> --json` returns one binding with its canonical ID.
These operator commands read locally configured paths.

To discover the coordinator's workspace IDs, use a server profile with an
administrator credential or a `workspaces:read` grant.

```sh
muxlio workspace list --server production --json
```

Remote listing returns canonical IDs, aliases, configured digests, and observed
owner status. It leaves local configuration untouched. If `hasMore` is true,
pass `nextOffset` with `--offset` to fetch the next page. See the
[workspace catalog API](architecture/service-reference.md#workspace-catalog-api)
for the status definitions.

Check the workflow, input, and selected workspace before submitting a run.

```sh
muxlio preflight workflow.ts input.json --server production --workspace-id dataset
muxlio preflight plan.json --server production --workspace-id data --json
```

Preflight imports and evaluates workflow source locally just like
`muxlio run --server`. Use source you trust, or supply a compiled Plan IR JSON
file to skip local source evaluation. Preflight accepts the same server
profiles, URLs, and bearer credentials. Omitted input defaults to `{}`.
The coordinator validates the plan and input, resolves workspace aliases, and
uses recent worker information to check executor requirements. For steps that
use a workspace, it also checks ownership and the content digest. The check runs
without creating a run, reserving capacity, or dispatching work. Provider access,
quota, and execution-time conditions remain unverified.

Use `--json` to get the complete `POST /v1/runs/preflight` response, including
executor diagnostics, worker evidence, and the lists of checks performed and
unverified or deferred checks. MCP `preflight_run` uses the same endpoint.
The CLI rejects responses over 32 MiB. It exits with code 0 when the coordinator
reports feasible, 1 when the plan is infeasible or the check fails, and 2 for
usage errors.

Submit `workspaceId` in `POST /v1/runs`, the MCP `start_run` arguments,
or the SDK workflow options. Remote CLI submissions accept `--workspace-id` on
`muxlio submit` and `muxlio run … --server`.
With a workspace registry configured, steps that need a source require an explicit
ID. Aliases such as `web` are stored as the canonical ID (`frontend`) in the run
and its attempts. The legacy single `workspace` configuration selects `default`.

Each identity belongs to one worker. Configure the same identity and owner in
that worker's runtime `config.json`, with a path that exists on that machine.
When two workers offer the same executor, such as `agent/codex`, selecting the
workspace selects its owner for steps that use that workspace. For a host and
sandbox example, see [Choose a worker by workspace](docker-sandbox.md#choose-a-worker-by-workspace).
Workers resolve symlinks at startup and check bindings again before execution.
Unknown IDs, unauthorized input paths, unavailable owning workers, and missing
worktrees fail closed; another checkout is never substituted. Client current
directories do not select worker directories. Prefer IDs without a `workspace`
input; if supplied, that input must match the authorized binding.

For reproducible handoff across machines, configure a binding `digest` and pin
the same `workspaceDigest` in the plan. `muxlio workspace digest <directory>`
computes the content identity for prepared files, including non-code workspaces.
See [prepared workspace handoff](architecture/workspace-handoff.md) for laptop
to remote worker provisioning and revision checks.

Workspace selection is independent of mode: `snapshot` copies the selected
source into attempt scratch, `empty` starts without a source, `read-only` requires
enforced isolation, and `shared-write` automatically acquires an exclusive lock
on the resolved workspace identity. Explicit `concurrencyKeys` remain available
for external resources. Workspace locks survive coordinator restart and worker
loss; uncertain writers hold their locks until completion or safe reconciliation.
Independent worktrees remain eligible to execute in parallel.

## Connect and run

Open **`http://127.0.0.1:7600/`** for the bundled UI. Follow [Setup defaults](#setup-defaults) for MCP; use the [MCP guide](mcp.md) for `integrations doctor`, remote HTTP, or repair. Then ask your assistant to build, validate, and run a workflow.
The worker needs the files being processed; the installed service does not need
Muxlio's source repository.

To keep a feature batch and its tools inside a local Docker sandbox, follow the
[Docker Sandbox walkthrough](docker-sandbox.md) for a worker in a private Git clone.
Read [Execution security → Docker Sandboxes](architecture/execution-security.md#docker-sandboxes-sbx-and-muxlio-workers)
for how outer `sbx` containment relates to coordinator tokens and per-attempt isolation.

## Manage the service

```sh
muxlio service stop
muxlio service restart
```

Restart after changing workspace or executor configuration. Logs are in
`~/.muxlio/logs/service.log` unless you selected another runtime directory.

If work does not start, run `muxlio doctor` and ask your assistant to inspect the
workers and run state. A disconnected chat does not cancel a running workflow.

For remote workers, configuration fields, security settings, and recovery behavior,
see the [service reference](architecture/service-reference.md).
