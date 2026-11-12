# USD recovery for bundled CLI adapters

This implements the conservative recovery policy reviewed on 12 November 2026.
Direct, attributable tool amounts precede local estimates. No source in this
implementation establishes invoice-backed cash payments. Cost lookups cannot
change the success or failure of agent execution.

Canonical accounting now retains bounded `sessions` evidence: provider,
provider-owned ID, source and observed CLI version, with optional execution
bounds, principal and isolation assertion. Reading `attributes` retain bounded
source classifications and monetary subcomponents. These fields carry no
credentials, prompts or transcripts. The coordinator and workers persist this
shared contract without interpreting provider protocols. The inspector renders
these fields generically.

## Cursor

The supported monetary source is the documented
[team usage-events API](https://prod.cursor.com/docs/account/teams/admin-api).
There is no browser-cookie, private database or undocumented dashboard fallback.
An eligible Admin API credential must be supplied to the adapter process as
`CURSOR_ADMIN_API_KEY`. It is removed from the executing agent's environment.
The key is never accepted in workflow params or written to accounting/logs.

The adapter preserves `system/init.session_id` as a provider session identity.
A model selection, output item ID, timestamp, or an account-wide balance is not
a substitute. If multiple init identities occur, recovery is rejected.

Before enabling a ledger join, an operator must validate that the installed
CLI's init ID equals the API `conversationId`, on isolated and overlapping
headless runs. Record the exact validated CLI version and account principal:

```json
{
  "model": "composer-2.5",
  "billing": {
    "verifiedJoinVersion": "<experimentally verified installed CLI version>",
    "email": "<authenticated principal>"
  }
}
```

Use `serviceAccountId` instead of `email` for a service-account credential;
never supply both. `verifiedJoinVersion` is an explicit operator assertion of
controlled validation. It is not inferred from a successful CLI run. A CLI
upgrade invalidates the assertion until repeated validation. There is no
bundled universally approved join version.

The API is queried only after checking credentials, join version and ownership.
The queried execution interval has inclusive millisecond bounds. Every accepted
event must match the owned conversation, configured principal, headless mode
and interval. Other conversations are excluded, even for the same principal.
No matching records means not-yet-available, never a zero charge.

All pages must be retrieved within bounded time/size limits, with consistent
counts and pagination. Authentication, schema, pagination, duplicate/ambiguous
request identities and invalid amounts safely leave cost unavailable. Provider
response bodies and credential values are never included in errors.

Two separate collected USD bases are retained:

- `provider-metered`: `chargedCents / 100`, the inclusive Cursor usage amount.
- `model-reference`: `tokenUsage.totalCents / 100`, its underlying model figure.

The selected reference metric prefers the inclusive metered amount for each
owned request; the underlying model figure is an alternative. A request-based
record can be valued without invented tokens. Explicit zero remains collected
zero. `cursorTokenFee` is recorded as an **included** subcomponent, never added
again. `kind`, `isChargeable`, token-based classification and discount metadata
remain source attributes. Included usage is not relabelled cash paid.

Amounts are shifted exactly from cents to USD with no floating-point round
trip. Unrepresentable collected precision is rejected rather than rounded.
The ledger does not replace the CLI's separately collected token readings.
Provider event models do not assign a single rate to the CLI's aggregate tokens.

### Late records and revisions

The initial response is **provisional**. Complete pagination and process exit
cannot prove that hourly provider billing records have settled. Cost coverage
therefore remains partial, including on successful runs.

The adapter also supports a read-only refresh command:

```sh
agent-cursor --reconcile-accounting < refresh-request.json > refreshed-accounting.json
```

The request contains `{"accounting": <prior final canonical snapshot>,
"billing": <the verified config above>}`. The credential remains in the
adapter environment. The result is a full canonical snapshot with a strictly
newer revision for the **same attempt**. The original owned session and execution
window are required; this command never runs another model request.

Refreshes re-read the entire original window and **replace** its ledger scope.
They do not append old/new monetary values or hash mutable amounts into new
charges. A failed refresh retains the previously confirmed subtotal and adds
a partial diagnostic. The command produces replacement evidence; it does not
silently edit a coordinator execution record or introduce an automatic billing
poller. A caller importing it must use the existing attempt's ownership and
revision controls. Schedule refreshes no more frequently than the provider's
hourly recommendation, with a trailing lookback for late-fill records.

Local validation: Cursor CLI `2026.11.11-043ef28` was installed, but the headless
probe returned authentication-required. No Admin API key was configured, so no
live conversation/ledger join or dashboard reconciliation was possible. This
account/version is **not production-qualified** by these tests. HTTP fixture
tests verify filtering, exact units, explicit zero, separate bases, failures and
snapshot replacement; they do not substitute for the live acceptance gate.

## Antigravity

The official [status-line contract](https://antigravity.google/docs/cli/statusline/)
defines `cost.total_usd` as a list-price estimate, including subagents. It is
not billed cash, even when `cost.estimated` is false. The documentation alone
does not establish headless availability.

Two concurrent isolated runs of CLI **1.5.6** used Muxlio's exact
`--prompt --output-format stream-json --dangerously-skip-permissions --add-dir`
flags, plus a session-only `--settings` status-line capture command. Both exited
successfully, exposed distinct `init.conversation_id` values and reported token
usage. The capture command was invoked **zero times**, and neither terminal
result contained monetary cost. The sanitized outcome is retained in
`muxlio-adapters/cmd/agent-antigravity/testdata/1.5.6-headless-cost-probe.json`.

The adapter now retains its init conversation identity and observed CLI version,
and explicitly reports `headless-monetary-source-unavailable` for the tested
version. Other versions remain `headless-monetary-source-unverified`. It does
not mistake context occupancy, quotas or tokens for dollars. Conflicting
conversation identities remain ineligible. No private local-history fallback
is enabled; such a fallback would need separate informed consent, versioned
format validation, owned requests and complete pricing evidence.

This is a negative observation for the tested version/account/headless mode,
not proof that cost is absent in interactive mode or every other account.
A future version requires a new headless-hook and attribution experiment before
its status-line monetary source can be enabled.

## Codex

The executing adapter retains `exec --json --ephemeral` and its fresh-thread
token normalization. It now retains `thread.started.thread_id` and observed CLI
version, and reports `served-model-and-tariff-unverified` for USD. Requested
models are not upgraded to served models. No rollout-file persistence or private
history scanning is introduced.

An additional controlled **0.167.2 App Server** probe used a fresh ephemeral,
read-only thread. Its thread/start response returned a configured model and
service tier. The usage update carried thread/turn identities, total/last token
breakdowns, cache and reasoning counts, but no independently confirmed served
model for each underlying request. The thread completed without a USD quote.
The sanitized numeric-only trace is retained in
`muxlio-adapters/cmd/agent-codex/testdata/0.167.2-app-server-accounting.jsonl`.

This demonstrates the documented live evidence surface, **not** a complete
per-model-call price ledger. No reroute/subagent attribution experiment was
qualified by that single run. The running adapter therefore remains unpriced
rather than multiplying aggregate tokens by a selected model's rate. A future
App Server execution path must prove model/tier-to-consumption linkage,
reroutes, subagent inclusion and ancillary charges before enabling pricing.
See [Codex App Server](https://learn.chatgpt.com/docs/app-server) and
[ephemeral non-interactive execution](https://learn.chatgpt.com/docs/non-interactive-mode).

## Validation boundaries

Provider-metered amounts, underlying model quotes and local tariff calculations
remain separate economic bases. A workflow reference summary selects one amount
per decoder-certified disjoint scope. Partial sources and unpriced attempts
cannot create a complete total, and refreshes cannot erase confirmed nonzero
consumption merely because a source temporarily fails.

Live multi-model, subagent, cancellation, delayed-ledger and dashboard/invoice
comparisons remain required before declaring a new source broadly complete.
No unsupported source is enabled by a passing fixture or a plausible dollar
number. Optional cookie endpoints, local provider databases and automatic
account-wide allocations are deliberately absent.
