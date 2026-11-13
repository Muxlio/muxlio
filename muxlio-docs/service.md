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

For multiple projects, configure `workspaces` in the service's `config.json`:

```json
{
  "workspaces": {
    "frontend": {"workerId": "local", "path": "/srv/frontend", "aliases": ["web"]},
    "backend": {"workerId": "build-host", "path": "/srv/backend"}
  }
}
```

Submit `workspaceId` in `POST /v1/runs`, the MCP `start_run` arguments,
or the SDK workflow options. Remote CLI submissions accept `--workspace-id`.
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
