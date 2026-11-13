# Delegation prompt acceptance

The canonical decision guide is `muxlio-skills/SKILL.md`. Client setup copies
that guide unchanged; the legacy Cursor rule changes relative links only.
Installation tests in `muxlio-core/internal/integrations/skills_test.go` check
that invariant for project/user skills and existing Cursor rule activation modes.

Run the focused offline rubric checks from the repository root:

```sh
node --test muxlio-testing/test/delegation.acceptance.test.mjs
```

The prompt matrix covers one-off work, an explicit small Muxlio request, durable
research/draft/verification, approval, scheduling, missing workers or workspace
access, simulation, accepted runs, worker re-entry, and explicit no-delegation.
Offline tests check the evaluator against regressions. They do **not** establish
that a model follows the guide. The real-client test skips unless configured.

To compare actual authoring clients, supply at least two external driver commands:

```sh
MUXLIO_DELEGATION_DRIVERS='{"client-a":["/absolute/driver-a"],"client-b":["/absolute/driver-b"]}' \
  node --test muxlio-testing/test/delegation.acceptance.test.mjs
```

Each driver receives one JSON document on stdin with `client`, `instructions`,
`prompt`, `context`, `vocabulary`, and `mode`. It must open a fresh session in
the named client, load the supplied shared instructions, submit the prompt, and
capture the native response and tool trace. Use sandboxed, deterministic mock
tools based on `context`: the reachable coordinator, advertised capabilities,
authorized workspace and preflight result are identical across clients. A
successful mock submission returns an accepted ID without terminal evidence.
Do not let the client execute real worker tasks, mutate a workspace, submit live
runs, or create schedules. These tests exercise authoring decisions, not live
worker validation. Client invocations may consume model quota.

Drivers emit JSON `{ "response": "native response", "semantics": { ... } }`.
Derive the semantic fields from the actual response and proposed actions/tool
trace using the supplied vocabulary; do not fabricate missing fields. Normalize
stage names to roles (research/draft/verify), omitting node IDs and native tool
syntax. `submissions` counts new run submissions in the mocked session, and
`recursiveDelegation` includes duplicate native and Muxlio delegation of the same
task. `preflight: required` means the proposed live path requires a feasible
preflight for the same plan/input/workspace. An approval-only draft may describe
that prerequisite without invoking it. `evidence: verified` requires observed
terminal success and checked real outputs; mock acceptance and fake tests cannot
justify it. Keep raw responses/traces in a caller-selected scratch directory.

The evaluator withholds expected answers from drivers, checks each client's
semantic projection against the same rubric, and compares those projections
across clients. Driver failures, malformed output, absent fields, mismatches,
and timeouts fail the test. This harness intentionally leaves native invocation
syntax and response extraction to client setup/test integrations rather than
encoding provider protocols in shared runtime code.
