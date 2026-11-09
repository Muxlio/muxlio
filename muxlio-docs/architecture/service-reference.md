# Service configuration reference

`muxlio service start` starts a background process that owns the coordinator,
one outbound local worker, static UI, and configured MCP HTTP listener. The UI
and coordinator share a port; MCP uses its own configured port. Logs go to
`logs/service.log` under the runtime directory. Use `muxlio service stop` or
`restart` to manage it. Under launchd, systemd, or another supervisor, set
`MUXLIO_DAEMON=0` to keep the service in the foreground. It runs with the
permissions of the account that starts it.

## Build and install locally

Requires Go matching `muxlio-core/go.mod` (currently 1.26). Runtime use requires
neither Go nor Node nor a source checkout. Agent provider CLIs and the tools
needed by the selected command adapter must separately be on PATH.

```sh
bash scripts/build-release.sh /private/tmp/muxlio-install
/private/tmp/muxlio-install/bin/muxlio version
# Install to a stable user prefix and make the CLI available in this shell:
bash scripts/build-release.sh "$HOME/.local/muxlio"
export PATH="$HOME/.local/muxlio/bin:$PATH"
```

The resulting prefix contains `bin/muxlio`, eight compiled adapters under
`libexec/muxlio/adapters` (`agent-a2a`, `agent-acp`, `agent-antigravity`, `agent-claude`, `agent-codex`, `agent-cursor`,
`agent-kiro`, and `command`), and UI assets under
`share/muxlio/ui`. Copy the
**whole prefix** to a stable location, for example `$HOME/.local/muxlio`, before
removing or moving the checkout. Relative resource discovery uses the installed
executable, never its build directory. No SDK is required to execute compiled
plans. Use the SDK/compiler from a development checkout to author plans.

The Homebrew formula uses the same release builder to install the current main
branch. See the [installation guide](../service.md#install). Maintainers can use
`scripts/release-formula.sh <version>` to prepare a tagged source release and
add its verified checksum to the formula.

## Configure and start

Create your authorized workspace and configuration before starting. Use absolute
paths, and select only the adapters you intend to permit. The service is a native
execution service for trusted users; a workspace is an execution target, **not a
filesystem sandbox**. Any client that can reach the service can submit plans
that invoke the advertised capabilities with your user permissions.

```sh
mkdir -p "$HOME/.muxlio/logs" "$HOME/.muxlio/workspaces/overnight"
chmod 700 "$HOME/.muxlio" "$HOME/.muxlio/logs"
cat > "$HOME/.muxlio/config.json" <<EOF
{
  "workspace": "$HOME/.muxlio/workspaces/overnight",
  "capabilities": ["agent/a2a", "agent/acp", "agent/antigravity", "agent/claude", "agent/codex", "agent/cursor", "agent/kiro", "command"],
  "attemptTimeoutSeconds": 600,
  "executors": {"agent/kiro": {"timeoutSeconds": 3600}},
  "mcp": {"listen": "127.0.0.1:7602"}
}
EOF
chmod 600 "$HOME/.muxlio/config.json"
muxlio init --workspace "$HOME/.muxlio/workspaces/overnight"
muxlio service start
```

`muxlio init` also registers the loopback MCP listener and generates persistent
client and worker tokens. It preserves existing credentials and MCP settings.
For a LAN listener, use `muxlio init --mcp-listen 0.0.0.0:7602` and restart the
service. MCP starts, stops, and restarts with the service; no second process is
needed. Per-executor `timeoutSeconds` overrides the shared adapter timeout for
that capability; explicit workflow timeouts can still impose a shorter limit.
Set `mcp` to `null` to disable it. See [MCP configuration](mcp-reference.md#network-transport-streamable-http)
for TLS, separate listener credentials, and client setup. Treat plain HTTP as
trusted-network access; use MCP TLS or a trusted TLS proxy for encrypted access.

Native adapters run with `HOME` set to the operator's real home directory by
default, so provider CLIs reuse existing login state already present there
(e.g. `~/.claude`, `~/.codex`, `~/.cursor`, Antigravity CLI config under your home). No extra configuration is needed if the agents
are already logged in under your normal home.

For **Cursor** on headless macOS hosts (SSH sessions, launchd user agents), the
login keychain stays locked, so the `agent` CLI must not rely on keychain
credentials. Log in once with on-disk credential storage:

```sh
AGENT_CLI_CREDENTIAL_STORE=file agent login
```

The Cursor adapter sets `AGENT_CLI_CREDENTIAL_STORE=file` for its child CLI (including when the coordinator runs under
`muxlio service` or launchd); values from `~/.zshrc` are not inherited. The
service does not unlock the keychain or change keychain lock settings.

### Shared worker environment

Set `workerEnv` once for all native executors. For example:

```json
{
  "workerEnv": {
    "PATH": "/Users/you/.local/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
  },
  "executors": {
    "command": {"allow": true}
  }
}
```

The environment starts with the worker defaults (including the launcher's PATH),
then applies `workerEnv`, then `executors.<capability>.env` overrides. These
settings apply to service workers, standalone runs, and remote workers on the
machine where their config is loaded. They are local operator configuration,
never workflow input or forwarded coordinator settings. The worker does not
inherit unrelated ambient environment variables. Configured shared values are
available to every native executor; keep executor-specific credentials in that
executor's override instead.

`muxlio init` saves the current PATH if `workerEnv.PATH` is missing and preserves
it on subsequent invocations. Set or update it explicitly with:

```sh
muxlio init --worker-path "$HOME/.local/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
muxlio service restart
```

A configured PATH must be nonempty and contain only absolute directories, with
no empty entries. Shell variables and `~` are not expanded in config values.
Capturing an SSH session's minimal PATH does not discover additional CLI install
locations; pass `--worker-path` when initializing from such a session. Once the
path is saved, restarting over SSH or under a supervisor preserves worker
executable lookup. It does not change the launcher environment for coordinator
or MCP-local commands.

`HOME`, `TMPDIR`, and `MUXLIO_*` remain worker-managed and are rejected in shared
and executor environments. Use `agentHome` for an authorized alternate native
HOME. Required isolation receives neither `workerEnv` nor executor environment
overrides; its existing isolation rules still apply.

When moving an existing repeated PATH to `workerEnv`, remove only per-executor
PATH entries identical to the shared value. Preserve different overrides,
other environment values, and permission settings such as `allow`.

### Feature commits

The implement-features recipe verifies changes and creates local Git commits in
the worker's workspace. It does not push, so the service needs no Git remote
credentials or upstream branch to complete a run. Authorize command execution
with `executors.command.allow: true` in `config.json` and restart the service
after changing configuration.

Review and push the commits manually from the worker when ready, using your
normal Git authentication. Commits remain on that machine until pushed.

Existing submitted runs retain their compiled plans. Removing the push step
from the recipe applies to newly compiled and submitted runs; it does not remove
that step from an older failed run.

### Dedicated provider credentials

To isolate provider credentials in a **dedicated** directory instead, log in
there once and set its absolute path as `agentHome` in config.json before
starting the service:

```sh
mkdir -p "$HOME/.muxlio/agent-home"
chmod 700 "$HOME/.muxlio/agent-home"
HOME="$HOME/.muxlio/agent-home" codex login
```

This is a provider login command you run interactively yourself; the service
does not run login or print credentials. For other providers use their login
command with this same dedicated HOME. Native adapters then read that explicit
credential directory instead of your real home. Required isolation always
retains scratch HOME and does not mount provider credentials.

Or run the installed absolute path when it is not on PATH. Keep the coordinator bound to loopback and use SSH forwarding for remote access. Client calls use `--token` or `MUXLIO_TOKEN`; remote workers use `--worker-token`, `--worker-token-file`, or `MUXLIO_WORKER_TOKEN`.

By default, `muxlio service start` runs in the background. Use `muxlio service
stop` or `restart` to manage it. For an external process supervisor, set
`MUXLIO_DAEMON=0` and configure that supervisor to restart the foreground process.
An advisory lock prevents concurrent services sharing one runtime directory.
Keep the execution host awake while work runs.

Runtime data is under `~/.muxlio` unless overridden: `data/coordinator.db`,
`logs/service.log`, `work/`, `artifacts/`, `service.pid`, `service.lock`, and
`config.json`. Installed binaries and static assets remain in the install prefix.
Use your host's log management to rotate service logs.

Configuration fields also accept `runtimeDir`, `addr`, `adapters`, `ui`, and
`agentHome`. To accept remote workers over a LAN or tailnet, set `addr` to a
**specific** non-loopback host and port with matching `tlsCert` and `tlsKey` PEM
paths (HTTPS on that address, plain HTTP on `127.0.0.1` with the same port). For
plain HTTP on every local interface (for example `0.0.0.0:7600` or `[::]:7600`),
use a wildcard `addr` without TLS. API authentication applies unless trusted-network UI access is enabled. Prefer
TLS when the coordinator is reachable from untrusted networks.
`--config PATH` or `MUXLIO_CONFIG` selects a configuration file.
Environment overrides: `MUXLIO_HOME`, `MUXLIO_ADDR`, `MUXLIO_ADAPTERS`, `MUXLIO_UI`,
`MUXLIO_WORKSPACE`, `MUXLIO_CAPABILITIES` (comma-separated), `MUXLIO_TLS_CERT`,
`MUXLIO_TLS_KEY`, `MUXLIO_SERVER`, `MUXLIO_TOKEN`, `MUXLIO_CONFIG`. Explicit environment values override configuration.
See the README Configuration section for the full field reference.

Standalone commands remain available. For real standalone execution, explicitly
select the project with `muxlio run plan.json input.json --adapters /path/to/adapters
--workspace /absolute/authorized/project`; `MUXLIO_WORKSPACE` is also accepted.
Standalone `server start` accepts its workspace through `MUXLIO_WORKSPACE` and
enforces the configured API authentication. Fake standalone runs require no workspace or provider
authentication.

## Submit tonight, check in the morning

`plan.json` must be a compiled Plan IR envelope member, not a JavaScript recipe.
The CLI sends the existing API envelope `{ "plan": ..., "input": ... }` and
returns immediately after durable acceptance. Closing the client or SSH session
does not cancel the server's work.

```sh
muxlio submit --server http://127.0.0.1:7600 plan.json input.json
# Save the returned runId, then disconnect the client.
muxlio status --server http://127.0.0.1:7600
muxlio status --server http://127.0.0.1:7600 run_RETURNED_ID
 muxlio pause --server http://127.0.0.1:7600 run_RETURNED_ID
 muxlio resume --server http://127.0.0.1:7600 run_RETURNED_ID
 muxlio cancel --server http://127.0.0.1:7600 run_RETURNED_ID
curl --fail http://127.0.0.1:7600/healthz
```

The UI offers the same run controls. Pause takes effect at the next operation
boundary; an already running operation is allowed to finish. Cancel stops
local adapter processes when possible. A remote worker may continue an
operation already assigned to it, so external side effects can occur after a
run is marked cancelled. The event history can be followed over
`GET /v1/runs/{id}/events/stream?after={sequence}` using Server-Sent Events;
reconnect with the last received event ID to continue without replaying prior
events.

Visit the configured UI URL to inspect runs. The `/healthz` endpoint is public; all `/v1/*` routes require the Bearer token generated by `muxlio init`.

Token usage is shown only when the agent CLI reports numeric token counts.
Attempts with no reported token counts show usage as unavailable. Credits
and context size are not converted into token totals. Older attempts with no recorded
counts remain unavailable; updating the adapter does not reconstruct them.

For remote use, prefer SSH port forwarding (encrypted, no public bind):

```sh
ssh -N -L 17600:127.0.0.1:7600 mac-mini
muxlio submit --server http://127.0.0.1:17600 plan.json input.json
muxlio status --server http://127.0.0.1:17600
```

With UI serving enabled, the service assumes a trusted local network and supplies operator access for `/v1/*` requests without a browser token. Explicit bearer credentials retain their scopes. Without UI serving, the API requires a Bearer token; the CLI attaches it automatically for loopback URLs from `~/.muxlio/config.json`. Pass `--token` or set `MUXLIO_TOKEN` for other endpoints.

## Recovery and limits

Every accepted run pins immutable plan JSON and its verified canonical digest.
Recovery checks those bytes, never a latest workflow definition. Queued work
that has not been assigned is eligible for recovery; executions whose effects
may already have happened are marked for manual reconciliation. This is not an
exactly-once guarantee for arbitrary external systems. Old assignment epochs
and generations are fenced; identical completion retries are acknowledged.

Attempts time out (default ten minutes), kill their process group, and can use
the plan's configured fallback executors. Automatic retries require the existing
effect policy; use `retry.maxAttempts`, `backoffSeconds`, `maxBackoffSeconds` to
control retries. Backoff for provider throttling is bounded. Timeout cannot undo
external effects: choose retries/fallback only for work whose effects tolerate
them. `effect: "retryable"` authorizes repeating effects; omitted effect preserves
the legacy retry contract. Other policies follow the declared effect contract. `pure` permits retries; keyed effects
require adapter deduplication support, and reconcilable effects require a read-only
lookup before redispatch after uncertainty. `manual`/`nonretryable` require operator
resolution. The retry budget is capped at 100 attempts per node. See the
[effect policy contract](worker-protocol.md#retry-safety-and-uncertain-external-effects).
Remote workers reconnect and re-register and execute assignments concurrently up
to their configured capacity.

Native child processes run with a minimal environment but `HOME` set to the
operator's real home directory, so they inherit existing provider login state
(e.g. `~/.codex`, `~/.cursor`) by default; set a dedicated `agentHome` to use a
separate credential directory instead. Required isolation always uses scratch
HOME and does not mount provider credentials. Other host secrets and
environment values are still not inherited. The worker protocol's explicit
request secret policy filters propagated secrets; there is no automatic host
secret lookup or secret forwarding from environment values. Providers using OS
keychains or environment-only credentials may require an audited custom
adapter. This feature does not claim automatic desktop credential forwarding.
Optional macOS VM isolation uses Docker Desktop with explicit opt-in, an approved
Linux image and the `desktop-linux` context; unavailable backends fail closed.
See [sandbox boundaries and conformance](execution-security.md).
Native trusted execution
does not promise network/filesystem containment. Back up the SQLite database
using SQLite-aware tooling. Retention, admission budgets, and scoped API credentials
are supported; they do not provide universal containment or exactly-once external effects.

Attempt start/finish events and bounded live progress are persisted during execution.
Workers stream supported adapter progress through the fenced log channel; final
captured logs also accompany completion. A missed lease or coordinator restart can
leave an external effect uncertain. The configured effect policy determines safe
replay, adapter reconciliation, or an operator hold. The `command` executor starts
in the selected attempt workspace; set `params.cwd` for a nested module. The default
workspace mode is a private snapshot; edits to the source require explicit
`shared-write` mode with coordinator-managed concurrency keys.

## Session input delivery

Adapters may opt into `sessionInput` revision `1` for durable input at a resumed
session's next turn, and `liveInput` revision `1` for input during an active
turn. Both use structured session revision `2`. Capability absence means
unsupported; the runtime never simulates steering by changing the pinned plan.
Bundled adapters do not currently advertise these input guarantees.

Submit input with `POST /v1/runs/{runId}/sessions/{sessionId}/inputs`:

```json
{"version":"1","requestId":"operator-message-1","message":"Focus on the failing check","policy":"live-or-next-boundary"}
```

The caller needs run-scoped `steer` permission and all queue permissions. The
optional `Idempotency-Key` header must equal `requestId`. Repeating the same
request returns the durable receipt; reusing its identity with different
content, policy or caller returns `409`. New input to a terminal run is rejected.
Unknown session input queues return `404`.

Policies are `next-boundary`, `live-only`, and `live-or-next-boundary`.
Unsupported live-only input returns `422`. Fallback input remains queued for
the next resumed turn when live delivery is unsupported or unacknowledged;
it does not create a turn automatically. The response is `202` with status
`accepted`, not a delivery claim. With run-scoped `read` permission, reconnect
and GET the same endpoint to inspect the versioned queue, chosen delivery
mode, timestamps and `accepted` versus `delivered` receipts.

Only an adapter's explicit `acknowledgedInputs` receipt marks input delivered.
The adapter must persist request-ID deduplication across live and boundary
delivery, including retries after lost receipts or restarts. Live transports
and provider limitations belong in the adapter. Workers dispatch the shared
`steer` action without acquiring the active session's turn lock or changing
its durable checkpoint. Boundary batches are pinned in durable dispatches;
new messages wait for a later turn. Queues and receipts survive coordinator
restart when SQLite storage is configured.

## Run schedules

The coordinator polls durable schedules at startup and every second. Use the
administrator bearer token to register a compiled plan with
`POST /v1/workflow-versions`, then create an immutable schedule:

```http
POST /v1/schedules
Content-Type: application/json
Authorization: Bearer <coordinator-token>

{
  "id": "nightly",
  "workflowDigest": "sha256:<registered-plan-digest>",
  "input": {},
  "kind": "cron",
  "startAt": "2026-11-06T00:00:00Z",
  "cron": "0 2 * * *",
  "timezone": "Europe/Dublin",
  "overlap": "queue",
  "maxCatchUp": 10
}
```

The digest must identify an existing, verified workflow version. Input is
required (including explicit `null` when appropriate) and validated against
that version's input schema. Schedule IDs use 1–128 letters, digits, underscores
or hyphens. Reusing an ID is rejected; definitions, target plans and inputs
cannot be edited. Registering a newer workflow version never changes a schedule.

`kind` is `once`, `interval` or `cron`. `startAt` is an inclusive absolute
timestamp. A one-time schedule fires at that timestamp. An interval additionally
requires `intervalSeconds` (1–315360000), anchored to `startAt` in elapsed UTC
time, without clock drift. A cron schedule requires five numeric fields
(minute, hour, day of month, month, weekday), supporting `*`, comma lists,
inclusive ranges and `/steps`, plus an explicit IANA `timezone` (not `Local`).
Sunday is 0 or 7. Restricted day-of-month and weekday fields use OR; when
either starts with `*`, both fields must match. Cron starts at the first
matching minute at or after `startAt`. Impossible expressions without a match
within eight years are rejected. Timezone data is bundled with the binary.

Cron uses local wall time with fixed DST policy: nonexistent times during a
spring jump are skipped; a repeated minute during a fall-back fires twice,
once at each distinct UTC instant. For example, `30 2 * * *` in
`America/New_York` does not fire on March 8, 2026; `30 1 * * *` fires at both
05:30Z and 06:30Z on November 1, 2026. Overlap and catch-up policy still apply
to those distinct occurrences. Intervals ignore DST entirely.

All schedules explicitly choose `overlap`:

- `allow` starts every eligible occurrence, including concurrent runs.
- `skip` records `overlap_skipped` when a prior run is still nonterminal.
- `queue` retains occurrences in a durable FIFO and starts the next only when
  the prior run becomes terminal. Paused and reconciliation-held runs block
  both `skip` and `queue`. Only runs from the same schedule affect overlap.

`maxCatchUp` (1–1000) limits how many overdue occurrences are considered in
one observation, including the current occurrence. The oldest are considered
first. Remaining overdue occurrences are permanently discarded, with a
`catch_up_limit` history record covering the inclusive UTC range from the
first discarded occurrence through the observation time. The cursor advances
past that range. This also applies when creating a schedule with a past start.
Queued occurrences already accepted by an earlier observation are preserved
and do not consume a later catch-up allowance. Queue size can grow when runs
consistently take longer than the recurrence interval.

`GET /v1/schedules` returns definitions and the next UTC fire (null once a
one-time schedule is exhausted). `GET /v1/schedules/:id/fires` returns persisted
decisions: `started` with a run ID, `queued`, `overlap_skipped`, or
`catch_up_limit` with `through`. A `started` receipt means the pinned run is
durably created; execution follows worker availability. Run creation, its
creation event, the fire receipt and the cursor commit in one transaction.
Retries and coordinator restarts reuse the same run identity for a UTC fire;
committed but unlaunched runs enter ordinary pinned-run recovery. Schedule
endpoints are administrator operations, unavailable to scoped run or worker
credentials. Schedules require SQLite storage and the running service.

## Verification

```sh
(cd muxlio-core && go test ./...)
(cd muxlio-adapters && go test ./...)
npm run build
npm test
npm --prefix muxlio-ui run smoke
bash scripts/build-release.sh /private/tmp/muxlio-install
bash scripts/verify-install.sh /private/tmp/muxlio-install
```

For runtime compiler imports in isolated worktrees with shared dependencies,
`node --loader ./scripts/workspace-loader.mjs ...` resolves local built workspace
packages without changing node_modules. TypeScript builds use local SDK types.
Run `npm run build` before Go tests to include the real async compiler regression.

Tests use temporary runtimes, ephemeral loopback ports and fake adapters. They
do not install a service or control any existing coordinator.

The bundled `agent/a2a` adapter discovers A2A 0.3.0 Agent Cards and uses
JSON-RPC to submit remote tasks. Set `params.url` to the remote agent origin,
optionally `cardUrl`, `stream: true`, and `timeout` (default `30s`, maximum `1m`).
Use the shared request secret mechanism for `A2A_TOKEN` HTTP bearer credentials.
Discovery, advertised capabilities, version and authentication are validated at
invocation. The readiness probe does not contact an arbitrary remote agent.

Use `effect: "reconcile"` and `requiredCapabilities: { remoteTask: "1" }`.
Task/context references survive local adapter exit; polls and reconciliation
query the known task without repeating submission. Streaming reconnects by task
ID and fetches a complete snapshot to recover missed artifacts. Unknown
submission outcomes without task IDs remain inconclusive; no provider-side
idempotency is claimed. See the [adapter configuration and limitations](developer-reference.md#run-with-real-adapters).
