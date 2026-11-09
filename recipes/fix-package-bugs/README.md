# Package bug sweep

Audit and repair Muxlio one package at a time. Like `implement-features`, this
recipe creates local commits for manual inspection and push. It does not launch
itself when compiled or tested.

Start a coordinator and a worker authorized for this checkout. This recipe uses
shared-write concurrency keys and must be submitted to the coordinator. From the
repository root, after `npm run build`:

```sh
node recipes/fix-package-bugs/discover.mjs --out recipes/fix-package-bugs/input.json
muxlio check recipes/fix-package-bugs/workflow.ts
muxlio run recipes/fix-package-bugs/workflow.ts recipes/fix-package-bugs/input.json \
  --server http://127.0.0.1:7600 --json
```

Discovery uses `go list` to enumerate each Go package in both modules, then adds
every npm workspace and the UI. Review or trim the generated package list to
control the sweep's scope and order. Go and Node dependencies must already be
available. Start from a clean checkout, keep generated input outside version
control, and run one writer workflow per checkout. Shared-write tasks require a
worker authorized for that checkout. Exclusive keys serialize operations, not
whole runs; they do not prevent another workflow or a person editing between steps.

Each item has an `id`, `path`, `kind`, `target`, and optional `context`:

```json
{
  "packages": [
    { "id": "core-plan", "path": "muxlio-core", "kind": "go", "target": "./internal/plan" },
    { "id": "sdk", "path": "muxlio-sdk", "kind": "npm", "target": "@muxlio/sdk" },
    { "id": "ui", "path": "muxlio-ui", "kind": "ui", "target": "." }
  ]
}
```

Use unique ids and workspace-relative paths. A Go `path` is the module directory
and `target` is its package relative to that module. For npm, `target` is the
workspace name. The `ui` kind uses `npm --prefix <path> test`.

For each package the workflow:

1. Requires a clean workspace, then audits code, callers and tests for concrete bugs.
2. Confirms findings, fixes bugs, and adds focused regression tests. No bug quota;
   unsupported findings are rejected explicitly.
3. Independently reviews the complete diff and runs package checks. Go packages
   run `go test` and `go vet`; npm workspaces run their build and tests.
4. Runs the repository build, workspace tests, UI tests and both Go test suites.
   A rejected review fails this gate even if every command succeeds.
5. Repairs failures, then repeats **both review and verification**, up to five
   attempts. Exhaustion pauses durably before committing or starting another package.
6. Commits only after the gate passes. A clean result succeeds without an empty commit.

The recipe uses the current workspace contract (`shared-write` with exclusive
keys), serial keyed maps, typed review outputs, artifact-backed findings and
check reports, bounded retry/backoff with executor fallback, execution and queue
timeouts, run-wide admission limits, durable refinement and guarded commits.
Agent steps use Kiro with Codex fallback, matching `implement-features`.
Agents can be retried and must inspect the existing diff before continuing;
commits are nonretryable and may require reconciliation after an interrupted
attempt. Provider protocols remain entirely in their adapters.

Artifacts and logs are visible in the run workbench and via `muxlio artifacts`;
the map output includes findings, initial repair notes, the successful check
report, attempt count and commit output. Review and later repair reports remain
in the run's intermediate artifacts. `checks.json` records each exact command,
exit code, signal and elapsed time; stdout/stderr remain in task logs. Accounting
uses executor-reported usage; this recipe does not estimate missing usage.

Inspect a paused run's events, reports and current diff before resuming. Keep
the checkout on the same branch throughout the run. This is an intentionally
thorough sweep: repository regression suites run for every package and can take
substantial time. Triggers, subscriptions and evaluations can wrap the compiled
plan through the normal runtime APIs without changing its package loop.

Run recipe validation without starting coding agents:

```sh
npm run build --workspace muxlio-recipes
npm test --workspace muxlio-recipes
# Also exercise serial execution and exhaustion in the real interpreter:
MUXLIO_TEST_CLI=/absolute/path/to/muxlio npm test --workspace muxlio-recipes
```
