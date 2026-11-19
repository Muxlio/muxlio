<p align="center">
  <img src="logo.png" alt="Muxlio" width="88">
</p>

<h1 align="center">Muxlio</h1>

<p align="center">Build a workflow. Let your agents run it.</p>
<p align="center"><sub>Durable workflow coordinator for AI agents and command-line tools</sub></p>

<p align="center">
  <a href="https://muxlio.github.io/muxlio/">Documentation</a> ·
  <a href="muxlio-docs/service.md">Get started</a> ·
  <a href="recipes/implement-features/workflow.ts">Implement-features recipe</a> ·
  <a href="LICENSE">Apache-2.0</a>
</p>

Muxlio is a **durable workflow coordinator** for multi-step work that mixes AI
agents (Claude, Codex, Cursor, Kiro, and others) with ordinary command-line
tools. You or your assistant describe the steps, inputs, success checks, and
outputs; Muxlio compiles that into an immutable **plan**, executes it on a
long-running **coordinator**, and keeps **runs** observable after your chat
disconnects.

Use it when work needs several dependent steps, verification, retries, or
hand-offs between agents and shell commands—not for one-off questions you can
answer in the client. Return later to the same run for status, logs, artifacts,
and anything that failed or paused.

A **workflow** (often shipped as a **recipe** in this repository) is the authored
logic; a **run** is one execution of that plan with your inputs. The
[architecture overview](muxlio-docs/architecture/overview.md) and
[developer reference](muxlio-docs/architecture/developer-reference.md#how-it-works)
spell out coordinator, workers, adapters, and Plan IR in more detail.

## One example: implement a list of features

I use Muxlio to develop Muxlio itself. The
[implement-features recipe](recipes/implement-features/workflow.ts) works through
features one at a time:

```text
Implement → Review → Run checks → Commit locally → Next feature
                         │
                         └─ if checks fail: Fix → Run checks again
```

The workflow runs the repository's build and tests before committing. After ten
unsuccessful verification attempts it pauses for attention. You can inspect the
change, review notes, test output, and execution history before deciding to push.
Each verified commit becomes the starting point for the next feature.

To run the batch in a private Git clone, follow the
[Docker Sandbox walkthrough](muxlio-docs/docker-sandbox.md). One sandbox worker
offers Claude Code, Codex, Cursor, and shell commands. The example takes two
dependent features from setup through preflight, execution, and fetching the
commits. The [live recipe](recipes/README.md#append-features-to-a-running-batch)
also lets you add features to the same branch while the batch is running.
If you set up both workspaces, choose `localhost` to run on the host or `sandbox`
to run on the `sbx` worker.

I keep Muxlio running on a Mac mini, queue work through my assistant, and review
progress later. You choose the machine: your workstation, another computer, or
a server. The execution host needs to stay running; your chat session does not.

This is one workflow, not a special product command. Adapt the
[recipe's tasks and checks](recipes/implement-features/tasks.ts) to your own project,
or build a workflow for research, data processing, documents, or other work.

## Install

With [Homebrew](https://brew.sh) available on macOS or Linux:

```sh
brew tap Muxlio/muxlio https://github.com/Muxlio/muxlio
brew install --HEAD Muxlio/muxlio/muxlio
muxlio version
```

This builds the current `main` branch and installs the CLI, MCP server, adapters,
and web UI. Homebrew handles the build dependencies; you do not clone the repo.
Install and sign in to the agent CLIs your workflows will use separately.

## Start with your own workflow

With Muxlio installed, initialize the workspace where work should happen:

```sh
muxlio init --client kiro --scope project
muxlio service start
```

Choose `--client` for your authoring assistant (`claude`, `codex`, `cursor`,
`kiro`, or `antigravity` for instructions only). `init` allows all bundled
executors by default, including agent tools, commands, workspace writes, and
network access. For supported clients, it also registers stdio `muxlio-mcp` in
that client's config. Change `capabilities` in `~/.muxlio/config.json` to limit
which executors the worker advertises; use the `executors` map for per-executor
policies. Repeated init preserves explicit settings and disabled executors.
`init` prepares workspace settings and credentials; `service start` runs the
coordinator, worker, bundled UI, and MCP HTTP listener. Default ports and MCP
client rules: [Setup defaults](muxlio-docs/service.md#setup-defaults).

After setup, verify stdio MCP with `muxlio integrations doctor` when the service
guide applies; see the [MCP guide](muxlio-docs/mcp.md). Ask your assistant to
build a workflow, validate it, and show you the plan before starting the run.
Developers can also author workflows directly with the TypeScript SDK.

### Author a workflow without a checkout

Installed releases support offline JSON authoring: run
`muxlio template echo > workflow.json`, then `muxlio check workflow.json`.
The `echo` template needs no executor; `muxlio template task` shows a generic
task with input and output bindings. Templates are embedded in the binaries
and shipped in `share/muxlio/templates`. With MCP, use `workflow_template`,
then `check_plan` and `start_run`. No Node packages or registry access are
needed for JSON authoring, validation, fake testing, or submission.

See [the installed authoring guide](muxlio-skills/references/authoring.md) for
an empty-workspace example and submission instructions. Optional TypeScript/
JavaScript compilation executes arbitrary code, requires
project-local dependencies installed from
your pinned lockfile, and never implicitly downloads packages.

The installed service does not need Muxlio's source checkout. Workers need the
files, authenticated agent CLIs, and tools required by your workflow.
[Source-build instructions](muxlio-docs/architecture/developer-reference.md#build-from-source)
are available if you prefer to build the runtime yourself.

## Follow the work

Open **http://127.0.0.1:7600/** or ask your assistant for run status. Inspect the
execution graph, attempts, live logs, and generated artifacts. Pause, resume, or
retry when appropriate. Uncertain external effects require reconciliation before
work can safely repeat.

To launch a registered workflow from the dashboard, select its version and an
authorized workspace, enter the inputs, and run preflight. See the
[dashboard guide](muxlio-docs/ui-operations.md#launch-a-workflow).

```sh
muxlio service stop
muxlio service restart
```

## Explore further

| Start here | Go deeper |
| --- | --- |
| [Which adapter should I use?](muxlio-docs/adapters.md) — native CLIs, ACP, and A2A | [Adapter capability reference](muxlio-docs/architecture/developer-reference.md#run-with-real-adapters) |
| [Service Guide](muxlio-docs/service.md) — setup and service controls | [Service reference](muxlio-docs/architecture/service-reference.md) |
| [Docker Sandbox walkthrough](muxlio-docs/docker-sandbox.md) — run dependent features in a private clone | [Worker protocol](muxlio-docs/architecture/worker-protocol.md) |
| [MCP Integration](muxlio-docs/mcp.md) — build and run from your assistant | [MCP tools and protocol](muxlio-docs/architecture/mcp-reference.md) |
| [Observability UI](muxlio-docs/ui-operations.md) — follow progress and results | [Operations API](muxlio-docs/architecture/operations-reference.md) |
| [Benchmarks](muxlio-docs/benchmarks.md) — what the suite checks | [Benchmark methodology](muxlio-docs/architecture/benchmark-methodology.md) |

Explore the [architecture](muxlio-docs/architecture/overview.md),
[execution and security contracts](muxlio-docs/architecture/execution-security.md),
and [CLI, SDK, and configuration reference](muxlio-docs/architecture/developer-reference.md).
The [example recipe](recipes/implement-features/workflow.ts) and
[authoring guide](muxlio-skills/SKILL.md) show how workflows are put together.

`muxlio init --client NAME --scope project --workspace /absolute/project` installs
the discoverable Muxlio skill and its references in that project. Use `--scope user`
for discovery across projects. See [client skill setup and invocation](muxlio-docs/mcp.md#install-and-invoke-the-muxlio-skill)
for supported paths, Cursor version handling, and preservation of existing edits.

## License

[Apache License 2.0](LICENSE).
