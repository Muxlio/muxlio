# Start Muxlio

Run Muxlio on the machine that will do the work. That can be your workstation,
another computer, or a server. Keep that machine running while workflows execute.

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
muxlio init --client kiro --scope project --allow-command
muxlio service start
```

Choose `--client` for the assistant you use: `kiro`, `codex`, `claude`, `cursor`,
or `antigravity`. It installs Muxlio authoring instructions and registers its local stdio MCP server.
`--agent` is a compatible alias for the authoring client, independent of workflow
execution adapters. Antigravity receives instructions only until its MCP CLI
contract can be verified. Use `--scope user` for user registration.
`--allow-command` explicitly permits command steps, including build checks and
Git commands. Omit it if your workflows do not need command execution.

`init` saves your workspace and prepares runtime directories, access credentials,
and MCP settings. `service start` starts the coordinator, local worker, web UI,
and MCP listener together in the background.

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

Open **http://localhost:7600** for the UI. Connect your assistant using the
[MCP guide](mcp.md), then ask it to build, validate, and run a workflow.
Run `muxlio integrations doctor --client kiro --scope project` to verify tool
calls through the client (requires login and trust approvals). See the MCP guide
for remote HTTP profiles, repair and removal.
The worker needs the files being processed; the installed service does not need
Muxlio's source repository.

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
