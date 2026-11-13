# Operations API reference

The UI exposes coordinator state through the shared executor contract. Executor
names are identifiers; capability and readiness reports determine what is shown.

## Screens

- **Operations** lists queued work and its recorded reason, attempts needing
  reconciliation, detached operations, recovery records, and active/failed runs.
  Counts cover the whole category; records use bounded pages.
- **Workers** includes stale workers, occupied slots, available accepting capacity,
  draining status, heartbeat health, expired active leases, readiness checks, and
  reported environment identities. These are current snapshots, not CPU or memory
  utilization estimates.
- **Automation** creates pinned one-time, interval, and cron schedules; inbound
  webhooks; and signed outbound subscriptions. Pause/enable changes operational
  state without deleting definitions or delivery evidence. History links to runs.
  Dead-letter redelivery requires an explicit review and submission. Secrets are
  write-only and are cleared from the creation form after saving.
- **Retention** manages run, plan, and artifact pins and explicit run retention
  deadlines. The displayed policy is the CLI's default policy; offline GC flags
  can override it. The default is 24 hours for event history, logs, artifacts, caches, and disposable run workspaces. Automatic maintenance checks every 15 minutes and proceeds only when there are no active engines, assignments, or HTTP requests. Manual GC retains its preview-and-approval operation.
  Removing a pin does not immediately delete data. Artifact revision history is
  available in a run's Artifacts tab and by run ID in the artifact library.

Resolution requires recorded operator evidence. A **not applied** outcome means
the old operation cannot commit later. A **completed** outcome additionally
requires a verified successful executor result. Resolution is separate from
choosing whether to retry or start a run.

## API additions

| Endpoint | Purpose |
| --- | --- |
| `GET /v1/operations?kind=attention&offset=0&limit=50` | Category counts and operational records; kinds are `attention`, `queue`, `detached`, `recovery`, and `runs`. |
| `GET /v1/workers?includeStale=true` | All registered workers, active assignments, server time, and heartbeat window. |
| `POST /v1/automation` | `{kind, id, enabled}`; kinds are `schedules`, `triggers`, and `subscriptions`. |
| `GET /v1/automation/history?kind=schedules&id=...&offset=0&limit=50` | Newest-first schedule fires or delivery metadata; `deadletters` does not require an ID. Delivery payloads are omitted. |
| `GET /v1/retention?offset=0&limit=50` | Paged pins and default retention policy. |
| `POST /v1/retention` | `{kind,id,pinned}` or `{kind:"run",id,retainedUntil}`; a null deadline restores the default policy. |
| `GET /v1/revisions?runId=...&before=0&limit=50` | Immutable intermediate snapshots, newest first, with `nextBefore`. |

The new global management endpoints use the coordinator's existing authorization
middleware; explicit run-scoped and worker credentials do not authorize them.
Automatic UI operator access is available to loopback peers and same-origin
private-network peers using a private IP Host.
Remote browsers require an authenticated gateway and scoped credentials; see
[remote web access](execution-security.md#remote-web-and-mcp-access).

New recovery attempts emit `run.recovered` with preserved, requeued, and uncertain
activation counts, or `run.recovery_blocked` with the recorded reason. Existing
histories are not rewritten to manufacture recovery events.

## Validation

`npm test --prefix muxlio-ui` includes operational rendering/accounting tests.
Backend API tests use isolated SQLite databases and test access boundaries,
pinning, revision cursors, evidence persistence, and automation pause/resume.

`npm run test:operations-browser --prefix muxlio-ui` is an optional Playwright
test. It uses local mock endpoints and never connects to the user's coordinator,
executes real workflows, or sends notifications. It expects an existing
Playwright installation; `MUXLIO_BROWSER_MODULE` may point to its module path.
No browser tooling is installed automatically by the application or test script.
