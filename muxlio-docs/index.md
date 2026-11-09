# Muxlio

A durable workflow execution engine that coordinates AI coding agents through
compiled, content-addressed plans.

Submit a multi-step workflow tonight — fan out across
[Antigravity](https://antigravity.ai), Claude, Codex, Cursor, or Kiro — and
check the results in the morning. The coordinator survives client disconnects;
your work does not stop when you close your laptop.

## Key features

- **Compiled, content-addressed plans** — Workflows compile to a stable Plan IR
  with a SHA-256 digest. Recovery verifies the exact bytes that were submitted.
- **Fan-out and control flow** — `map`, `branch`, `loop`, `wait`, `join`, and
  `child` give you full workflow composition without managing concurrency by hand.
- **Durable execution** — SQLite-backed state, event-sourced history, and
  explicit recovery. Queued work is re-eligible for assignment; effects that may
  have already happened are held for manual reconciliation.
- **Retry and fallback** — Per-step retry policies, bounded backoff, and
  fallback executors. `onNonZero: 'return'` lets workflows self-correct.
- **Agent adapters** — Ships adapters for `agent/a2a`, `agent/acp`,
  `agent/antigravity`, `agent/claude`, `agent/codex`, `agent/cursor`,
  `agent/kiro`, and `command`.
- **Observability UI** — Run search, activation/scope graphs, event timelines,
  logs, attempt history, a Live view for streaming output, and a signal form.
- **MCP server** — Any MCP-capable assistant can build, submit, and inspect
  runs directly from its chat.

## Quick links

- [Service Guide](service.md) — running the coordinator as a background service
- [MCP Integration](mcp.md) — wiring the MCP server into your AI assistant
- [Observability UI](ui-operations.md) — navigating the dashboard
- [Architecture Overview](architecture/overview.md) — component map and design
  decisions
- [Error Reference](errors/errors.md) — coordinator and adapter error codes

## Install

```sh
brew tap Muxlio/muxlio
brew install muxlio
```

Or build from source:

```sh
git clone https://github.com/Muxlio/muxlio.git
cd muxlio
npm run setup   # builds coordinator, adapters, UI → ~/.muxlio and runs muxlio init
```

See the [full README on GitHub](https://github.com/Muxlio/muxlio#readme) for
the complete getting-started guide, SDK usage, and workflow authoring reference.
