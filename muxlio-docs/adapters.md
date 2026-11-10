# Which adapter should I use?

Muxlio runs workflow steps through **executor capabilities** (for example
`agent/codex` or `agent/a2a`). Each capability is implemented by an **adapter**
binary on the worker. Pick an adapter based on where the agent runs, how you
authenticate, and which protocol features you need.

This guide explains the three onboarding paths. **Configuration limits and
protocol details for ACP and A2A remain authoritative in the
[CLI and SDK reference — Run with real adapters](architecture/developer-reference.md#run-with-real-adapters).**
A2A recovery and polling are summarized under
[Recovery and limits](architecture/service-reference.md#recovery-and-limits) in the
service reference.

## Three ways to run an agent step

### Native CLI adapters (recommended when a provider CLI is installed)

Shipped executors: `agent/antigravity`, `agent/claude`, `agent/codex`,
`agent/cursor`, and `agent/kiro`.

Each adapter runs that vendor's installed CLI in the attempt workspace. Setup is
the same as using the CLI directly: install it, sign in, and list the capability in
`~/.muxlio/config.json` if you restrict workers. Steps run **locally** on the
worker. Provider streaming and tool activity appear in Muxlio live logs when the
CLI emits them (`progress` capability). These adapters report **token usage** when
the provider supplies it; they do not treat context-window occupancy as consumed
tokens.

Use a native adapter when you want provider-specific behavior (models, tools,
billing) without implementing a protocol yourself.

### Generic local agents — Agent Client Protocol (ACP)

Executor: `agent/acp`.

Runs any **ACP protocol version 1** agent over stdio (`program` + `args` in task
params). The agent must speak JSON-RPC on stdin/stdout. This path is for
**local** agents you build or install separately (placeholders such as
`/absolute/path/to/acp-compatible-agent` in examples).

Setup highlights:

- **Executable and args** — passed directly; no shell.
- **Host authorization** — `executors.agent/acp.allow: true` in worker config
  (`requiresHostExecution`).
- **Authentication** — configure the agent's credentials in the executor
  environment; authenticate the agent **before** running workflows (Muxlio does
  not invent ACP login commands).

ACP negotiates **protocol v1** only. Permission prompts, durable sessions,
session loading, client FS/terminal services, remote tasks, and native typed
generation are **unsupported** even if the agent advertises them. Token usage is
**unavailable**; context-window or cost hints from the agent are not token
totals.

### Remote agents — Agent2Agent (A2A)

Executor: `agent/a2a`.

Talks to a **remote** agent over HTTP using **A2A 0.3.0** JSON-RPC. The worker
submits work and can exit while the remote task continues (**remote task**
capability). Durable workflows should set `retry.effect` to `reconcilable` (or
`reconcile`) and `requiredCapabilities: { remoteTask: "1" }` so polls and
reconciliation use the remote task identity without resubmitting.

Setup highlights:

- **Agent Card** — discovered at `/.well-known/agent-card.json` from `params.url`
  (optional `cardUrl` on the same origin).
- **Bearer secret** — map `A2A_TOKEN` through the worker
  [shared request secret](architecture/execution-security.md) mechanism, not workflow
  parameters or URLs.
- **Remote capabilities** — the card must expose a compatible JSON-RPC interface;
  `stream: true` requires advertised streaming.
- **Cancellation** — local cancel stops the adapter process; it does **not**
  cancel remote work.

Submissions without a task ID stay **inconclusive** on reconcile. The adapter does
not advertise **idempotency keys** (A2A message IDs are not deduplication
guarantees). Token usage is **unavailable**.

## Capability comparison (bundled adapters)

Values below come from shipped `adapter.json` manifests and adapter behavior as
of this release. **Unsupported** means the capability flag is absent or false and
the adapter must not emulate it. **Unavailable** for accounting means usage/cost is
not reported (missing usage stays nil; reported zero is zero).

| Capability | Native CLI (`agent/claude`, `codex`, `cursor`, `kiro`, `antigravity`) | `agent/acp` | `agent/a2a` | `command` |
| --- | --- | --- | --- | --- |
| Protocol | Provider CLI (no single protocol version) | [ACP v1](https://agentclientprotocol.com/protocol/v1/initialization) | [A2A 0.3.0 JSON-RPC](https://a2a-protocol.org/v0.3.0/specification/) | Child process argv |
| Setup / auth | Install + sign in to vendor CLI; optional `agentHome` | `allow: true`, agent env + out-of-band agent login | `A2A_TOKEN` bearer secret, HTTPS (loopback HTTP allowed) | `allow: true` for host execution |
| Execution locality | Local worker | Local stdio child | Remote HTTP agent | Local child process |
| Artifacts / outputs | Yes (`artifacts`) | Yes | Yes (bounded; no URI fetch) | Yes |
| Live progress / streaming | Yes (`progress`); provider-dependent | Yes (session updates in logs) | Yes (`progress`); optional `stream: true` + resubscribe | Yes (command output previews) |
| Mid-run cancel | No (`cancel` not advertised) | Yes (`session/cancel`) | No remote cancel; local stop only | Yes (signal child) |
| Durable sessions | No | No (ACP session IDs are not Muxlio checkpoints) | No | No |
| Interactive permissions | Provider-defined | Denied explicitly | N/A (remote agent) | N/A |
| Remote task handoff | No | No | Yes (`remoteTask`) | No |
| Reconcile / idempotency | No reconcile; no `idempotencyKey` | No | Reconcile yes; idempotency keys **not** advertised | No |
| Token / credit / cost reporting | Yes when provider reports usage (`usage`) | Unavailable | Unavailable | Unavailable |
| Host execution gate | No | Required | No | Required |

!!! note "Do not expect full protocol coverage"
    ACP and A2A adapters implement the subsets documented in
    [Run with real adapters](architecture/developer-reference.md#run-with-real-adapters).
    Unsupported card extensions, auth modes, input modes, or protocol versions fail
    closed at invocation.

## Minimal examples

Compile and dry-run without live agents using the fake executor (same pattern as
other `examples/` workflows):

```sh
npm run build
muxlio check examples/acp.workflow.ts --json
muxlio check examples/a2a.workflow.ts --json
muxlio test examples/acp.workflow.ts --input examples/acp.input.json \
  --fake-config examples/acp.fake.json --json
muxlio test examples/a2a.workflow.ts --input examples/a2a.input.json \
  --fake-config examples/a2a.fake.json --json
```

### ACP — local stdio agent

Workflow: [`examples/acp.workflow.ts`](https://github.com/Muxlio/muxlio/tree/main/examples/acp.workflow.ts).

1. Build or install an ACP v1-compatible agent binary.
2. In `~/.muxlio/config.json`:

```json
{
  "executors": {
    "agent/acp": {
      "allow": true,
      "env": {
        "MUXLIO_ACP_PROGRAM": "/absolute/path/to/acp-compatible-agent"
      }
    }
  }
}
```

3. Authenticate the agent using its own instructions, then run:

```sh
muxlio run examples/acp.workflow.ts examples/acp.input.json \
  --server http://127.0.0.1:7600 --json
```

Replace `params.program` in the workflow or rely on `MUXLIO_ACP_PROGRAM` for the
default executable used by the probe.

### A2A — remote Agent Card

Workflow: [`examples/a2a.workflow.ts`](https://github.com/Muxlio/muxlio/tree/main/examples/a2a.workflow.ts).

1. Deploy or select an A2A 0.3.0 agent with `remoteTask` support on the card.
2. Configure a worker secret handle that resolves to `A2A_TOKEN` (see
   [execution security](architecture/execution-security.md)). For example, map
   the handle to the name of an environment variable that contains your token:

```json
{
  "executors": {
    "agent/a2a": {
      "secretEnv": { "a2a-production": "A2A_TOKEN" }
    }
  }
}
```

   The example workflow declares `secretRefs` and allows that secret name; the
   token value never belongs in the workflow or its input file.
3. Set `params.url` to the agent origin (placeholder `https://agent.example` in
   the example). The adapter discovers the Agent Card at
   `/.well-known/agent-card.json`; the card must advertise A2A 0.3.0 and a
   compatible JSON-RPC endpoint.
4. Run through the service so reconcile and polling apply. Muxlio polls by the
   durable remote task identity. Local cancellation stops its adapter process
   but leaves remote work running:

```sh
muxlio run examples/a2a.workflow.ts examples/a2a.input.json \
  --server http://127.0.0.1:7600 --json
```

For ambiguous remote outcomes, see
[Recovery and limits](architecture/service-reference.md#recovery-and-limits) and
[Run with real adapters — agent/a2a](architecture/developer-reference.md#run-with-real-adapters).

## Providing a compatible agent

| Path | You provide |
| --- | --- |
| Native CLI | Vendor CLI on PATH or in `agentHome`, already authenticated |
| ACP | Executable implementing ACP v1 initialize → `session/new` → prompt → declared output files under paths Muxlio passes |
| A2A | HTTPS (or loopback) origin with valid Agent Card, JSON-RPC task API, artifacts named for output ports; bearer auth if required |

Conformance fixtures in `muxlio-adapters/tests/conformance/` exercise ACP and A2A
adapters without paid external services.

## Documentation site (GitHub Pages)

Published docs: **https://muxlio.github.io/muxlio/**

- Build: `pip install -r .github/requirements-docs.txt && mkdocs build --strict`
- Deploy: `.github/workflows/docs.yml` on pushes to `main` (docs paths only).
  Pull requests **build and upload a preview artifact**; they do **not** deploy.
- **Repository setting (one-time):** GitHub → Settings → Pages → **Build and
  deployment → Source: GitHub Actions** (not a branch). No extra permissions are
  required beyond the workflow's scoped `pages: write` on deploy jobs.
