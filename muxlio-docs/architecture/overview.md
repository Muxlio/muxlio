# Muxlio Architecture Overview

Run timing has two definitions: **Elapsed time** is wall-clock time from run creation to completion (or the current time while active), including queueing, pauses, and waits. **Total execution time** is the sum of worker-measured executor durations across attempts, including failures and retries, excluding coordinator dispatch queues, pauses, retry backoff, and external waits. Parallel worker durations add together and can exceed elapsed time. Timing becomes available when an attempt returns; active totals include the measured attempts completed so far. Historical attempts without worker timing make the total unavailable, rather than showing an incomplete sum as a complete total.

The [durable execution contracts](durable-execution.md) define run, activation,
attempt, plan-version, replay, and waiting semantics.

## System boundary

Muxlio executes immutable workflow plans against durable state. Workflow
authors choose what work to do; adapters perform effects; the coordinator
knows only execution; the independently run UI observes it through the API.

## Components

### muxlio-core (Go)
The execution engine. Contains:
- **internal/plan/** — Plan IR types, expression evaluator, validator, interpreter
- **internal/state/** and **internal/storage/** — state contracts and SQLite persistence (runs, activations, events, signals, dispatches)
- **internal/workers/** — executor interface, script executor, fake executor
- **internal/artifacts/** — content-addressed artifact store
- **internal/coordinatorlog/** — structured lifecycle logging (redacts secrets)
- **internal/api/** — HTTP coordinator API (Bearer-token auth on `/v1/*`)
- **internal/sandbox/** — execution isolation backends and boundary checks
- **internal/schedule/**, **internal/subscription/**, and **internal/trigger/** — durable schedules, outbound lifecycle subscriptions, and inbound webhook triggers
- **internal/telemetry/** and **internal/diagnostic/** — OpenTelemetry integration and structured diagnostics
- **cmd/muxlio/** — CLI entry point and service supervisor
- **cmd/muxlio-mcp/** — standalone MCP server

The coordinator exposes only the HTTP API. The standalone **muxlio-ui** package
serves the observability page and reverse-proxies `/v1/*` and `/healthz` to a
configurable coordinator URL (`MUXLIO_API_URL`, default
`http://127.0.0.1:7600`). Run it with `npm --prefix muxlio-ui start`; it listens
on port 7601 by default.

### muxlio-sdk (TypeScript)
The symbolic builder (Frontend B). Authors workflows declaratively and
compiles them to Plan IR JSON. Provides expression helpers, typed bindings,
and `defineWorkflow`.

### muxlio-compiler (TypeScript)
The restricted async TypeScript frontend (Frontend A). Authors workflows
using `async run(ctx, input)` syntax with `await ctx.execute(...)`. It records
workflow operations at compile time to build the plan.

### muxlio-testing (TypeScript)
Test harness with fake executors. `testWorkflow` runs a compiled plan
against the Go CLI with canned outputs — no model tokens required.

### Go tests (`muxlio-core/tests`)
Conformance, contract, coordinator, integration, and recovery tests cover stable
scope identity, output reference resolution, branch/loop semantics, worker and
adapter contracts, persistence, and event generation.

## Plan IR v1.0

The IR is the canonical contract between TypeScript authoring and Go
execution. It is a JSON document with:
- Version, name, input/output JSON Schemas, content digest
- Named subgraphs, each containing nodes and an output projection
- Node kinds: execute, map, branch, loop, join, wait, child, project
- Bindings (value references or expressions) that create dependency edges
- A pure expression language (field access, comparisons, boolean logic)

## Version evolution and compatibility

Workflow source versions and Plan IR versions are separate. Editing and
recompiling a workflow creates a new immutable plan; its canonical digest is
the version identity. A run pins the exact plan digest it was created with.
Uploading a newer plan under the same human-readable workflow name is an
addition to the catalog, never an update to an existing digest or run.

Compatibility is checked before execution. The coordinator accepts only the
IR versions whose semantics it implements (currently `1.0`); it rejects
unknown, future, or otherwise unsupported versions with
`MUXLIO_UNSUPPORTED_IR_VERSION`. A shared major number alone is not evidence
of compatibility. IR changes that alter existing field meanings or execution
semantics require a new major version. Additive changes can use a minor version
only when older readers can safely ignore them; coordinators still must
explicitly advertise/support that minor version before accepting it. SDK,
compiler, worker protocol, and executor versions evolve independently.

Runs retain their pinned plan for their full lifetime, including after source
edits or newer plans are published. Resume/recovery uses the original plan
and its compatible interpreter; it must never substitute the latest plan.
If a coordinator cannot interpret a pinned plan, it leaves the run's plan
unchanged and reports an incompatibility requiring a compatible coordinator
or an explicit new run. Migrating a workflow means publishing a new plan and
starting a new run (or an explicit fork); it never rewrites a plan already
referenced by a run. Retain plan bytes as long as any run or retention policy
references their digest.

## Dataflow model

Each activation publishes immutable named outputs. Downstream nodes bind
to these outputs via `ValueRef { node, port }`. The interpreter processes
nodes in topological order (derived from bindings). Map creates child
scopes per item; branch selects one path; loop carries state across
iterations.
