# Accounting

Adapters, persisted attempts, worker completions, API responses, CLI output, and
TypeScript clients share canonical accounting. Protocol identifiers remain v1.

Snapshots have a positive monotonic `revision`, `final`, disjoint `entries`, and
per-meter `coverage`. Each snapshot replaces the preceding one. Replaying an
identical revision is idempotent; conflicting bytes at that revision are rejected.
Distributed writes check assignment, epoch, generation, and lease transactionally.
Confirmed accounting survives an absent terminal snapshot and coordinator restart.
Accepted progress emits `execution.accounting.updated` with only `attemptId`,
`revision` and a canonical SHA-256 `hash`. Subscribers refetch the snapshot;
they do not reconstruct an additive ledger from events. Identical replays do
not emit another update.

Worker completion commits its canonical accounting snapshot and receipt in one
fenced transaction before acknowledgment. A conflicting or stale revision rejects
the completion without sealing its receipt; a failed receipt write rolls back the
accounting update too. Retrying an identical accepted completion is idempotent.

```json
{
  "revision": 2,
  "final": true,
  "entries": [{
    "id": "call-1",
    "tokens": {
      "input": {"collected":{"value":"1200","source":"cli-turn","fidelity":"metered"}},
      "output": {"collected":{"value":"400","source":"cli-turn","fidelity":"metered"}},
      "cacheRead": {"collected":{"value":"500","source":"cli-turn","fidelity":"metered"}}
    },
    "credits": [{"provider":"service","unit":"credit","amount":{
      "collected":{"value":"0.18065595854063016","source":"provider-turn","fidelity":"metered"}
    }}]
  }],
  "coverage": [{"key":{"kind":"tokens"},"state":"partial"}]
}
```

Input includes cache read and cache write. Output includes reasoning. Subsets are
never added again to input plus output. Total-only reports do not invent an
input/output split. Counts and amounts are decimal strings throughout storage and
the API. A reported zero is `"0"`; missing readings remain unavailable.

Measures can carry `collected` and `estimated` evidence. Collected means acquired
from a source and does not certify an invoice. Fidelity distinguishes `metered`,
`derived-exact`, `provider-estimate`, and `proxy`. Local estimates require a method
and version. Proxy readings cannot establish full token consumption.

Costs are keyed by currency and basis: `api-equivalent`, `plan-debit`, or
`unspecified`. API-equivalent estimates and plan debit remain separate. Credits
retain their provider and unit. Credits and context occupancy are not converted
into spent tokens or USD.

Run summaries select collected readings first, then eligible estimated fallback
for disjoint scopes. They expose `collectedSubtotal`, `estimatedFallbackSubtotal`,
`knownSubtotal`, and `effectiveTotal`, with coverage and attempt counts. Subtotals
can describe incomplete consumption. Complete totals require all relevant scopes
to be closed and covered. Failed attempts retain consumption; pending and uncertain
attempts prevent final totals. Undeclared applicability remains unknown.

Limits select explicit meters under `limits.accounting`, with decimal maxima.
Defaults require collected evidence, any fidelity, and fail closed on missing
accounting. Credit limits require provider and unit; cost limits require currency
and basis. Metered policies reject local estimates. Limits stop later admission;
already running concurrent work can overshoot a threshold.

Analytics reduces persisted disjoint attempts on the server. Completed amounts
belong to their UTC completion date. Chart geometry can be approximate; labels and
evidence retain exact decimal strings. OpenTelemetry provider-token attributes
are emitted once from finalized, complete collected dimensions. Incomplete and
locally estimated counts are omitted from provider-token signals.

Finalized complete collected dimensions also emit the OpenTelemetry
`gen_ai.client.inference.usage.*` counters for inclusive input/output and reported
cache/reasoning subsets. A queued coordinator does not emit its worker's usage
again. Final owned remote-task snapshots emit once when accepted; repeated polls
do not add another measurement. Zero remains explicit in snapshots and span
attributes; the usage counters record positive contributions only. Modality is
`unknown` when the source does not identify it. These instruments follow the
[OpenTelemetry token conventions](https://github.com/open-telemetry/semantic-conventions-genai/blob/main/docs/gen-ai/gen-ai-token-metrics.md).

Selected local estimate fallbacks use separate `muxlio.accounting.estimated.*`
diagnostics. Economic metric projections use approximate floating-point values;
the persisted decimal strings remain exact. Metric dimensions contain only meter
identity, fixed token categories and coverage, without run, task or account IDs.

Provider fixtures validate parsing without credentials. Complete coverage claims
also require controlled live probes with recorded binary versions and source
semantics. Unverified bundled CLI readings retain partial or unknown coverage.
The shared recorder compacts disjoint events by provider, actual model, meter
presence and provenance after pricing. It retains event identities after terminal
replacement, so old deltas cannot reappear. Limits are 100,000 identified events,
256 compact buckets and 64 KiB per snapshot. Overflow retains confirmed readings
and degrades coverage with `accounting_capacity_exceeded`. Progress writes are
serialized and bounded; remote delivery retries identical canonical snapshots
and advances its acknowledgment only after persistence succeeds.

Analytics accounting covers the returned run page, including its per-attempt,
daily, workflow and executor summaries. Charts show the same page; previous/next
navigation retrieves another page without an automatic scan of all matching
runs. Daily consumption is attributed to attempt completion dates in UTC.

Per-call API-equivalent pricing uses `accounting.RecordPriced` before compaction.
`accounting.LoadCatalog(raw, digest)` loads a reviewed JSON catalog;
`accounting.PinCatalog` validates and generates its immutable SHA-256 ID. Catalogs
are at most 1 MiB and never fetched during execution. Each rule selects an actual
provider/model, service tier, mode, cache-write TTL, context interval and effective
date interval. Rates distinguish uncached input, cache reads/writes, visible
output, reasoning and additional tool/multimodal charges. Missing applicable
rates, quantities, identity or charge applicability make the call unpriceable.
Explicit zero rates remain valid. Arithmetic is exact, rounded half-even once
to 18 decimal places. Stored estimates retain their rule and catalog IDs.

Provider estimates retain their fidelity in server summary evidence. The UI
labels collected provider estimates explicitly and shows overlapping local
calculations as alternatives in attempt details, never as an additional charge.

Bundled CLI fixtures do not prove all per-request model, tier and extra-charge
categories. Local pricing therefore stays unavailable for those streams rather
than pricing aggregate tokens or a requested `auto` model. Their collected
provider costs remain available independently. A catalog alone cannot establish
complete coverage. Enabling a provider price estimate requires a decoder fixture
and controlled live evidence of the actual request categories. No calibrated
credit/context-to-token estimator is enabled.

Kiro supplements collected token readings with local `cl100k_base` BPE proxies.
The adapter enables `KIRO_DUMP_REQUESTS` in a private temporary directory for
each invocation. It counts text, tool definitions, tool-call arguments and tool
results from every captured request, including the history actually retained
after compaction. Repeated history counts on each request; background requests
captured during the invocation contribute input too. It does not scan shared
session history or infer consumption from context percentages or credits.

Output estimates tokenize streamed assistant text, exposed reasoning and the
latest arguments for each tool call. Chunks are joined before tokenization;
legacy text and terminal echoes are fallback sources rather than extra output.
These estimates use `fidelity: proxy`, method `cl100k_base-bpe`, and version
`tokenizer-0.7.0-kiro-content-v1`. The bundled vocabulary needs no runtime download.
Collected readings retain precedence, including explicit zeros. Input remains
unavailable when request capture is absent or invalid. Neither cache usage nor
a complete token total is invented. Capture files are removed before returning
success or failure; only numeric evidence enters accounting.

Kiro CLI 3.2.0 with agent engine 2.37.0 was probed with a tools-disabled prompt
to verify the capture shape and background title request. The proxy tokenizer
is not Kiro's served-model tokenizer, and capture does not establish coverage
of server-added context, hidden reasoning, transport retries, multimodal tokens
or background output. Estimates therefore retain partial coverage, with reason
`tokenizer-proxy-incomplete`; they are not calibrated billing measurements.

Snapshots preserve bounded `models` reports with an ID, source and role:
`requested`, `selected`, or `served`. A configured default, display label or
`auto` selection does not establish the actual served model. Claude terminal
`modelUsage` keys report served models; Cursor init and ACP model configuration
report selections. Kiro retains its selected configuration and requested model.
Codex and Antigravity retain an explicitly requested model, when supplied; their
tested streams omit served-model identity. A2A has no accounting or invented
model extension. Model metadata alone never creates consumption or a price.

When a lost worker is replayed, its last accounting snapshot is preserved in a
separate failed execution record before the replacement starts a fresh revision
sequence. Non-final usage remains incomplete. Replay checks pinned work and
accounting limits; a blocked replay returns a durable failure to the waiting
coordinator. Old worker generations cannot update the replacement's readings.

A2A has no supported accounting source. Task metadata and artifacts are never
interpreted as consumption; its accounting remains unavailable. Executors that
explicitly emit canonical accounting can use the generic remote-task ownership
checks without changing A2A behavior.

ACP's `usage_update.used` and `size` describe context occupancy, not consumed
tokens. Session-cumulative cost requires a same-session baseline received before
prompting; a first sample during execution remains unattributed, and a counter
reset invalidates the baseline. Monetary units retain cost basis `unspecified`.
Nonmonetary units require the adapter parameter `meteringAuthority` to identify
the backend's meter; the reported unit is preserved.

## USD reference valuation

The default USD metric is **Estimated reference value (USD)**. The shared
`referenceValue` summary selects one eligible amount per canonical disjoint
entry, in this order: directly collected attributable USD quote, published model
rate calculation, credit replacement calculation. Direct quotes, including zero,
prevent redundant local pricing. Original cost partitions and reading provenance
remain available. Equally ranked overlapping alternatives within an entry are
ambiguous and contribute no value. Adapters must collapse overlapping invocation,
per-turn and per-model observations before publishing canonical entries.

Reference dollars describe consumed services, not actual account spend or an
invoice. `plan-debit` is retained separately and is not promoted to reference
value. Non-USD quotes retain their original currency; there is no implicit FX.
A mixed-method summary discloses its methods. Missing attempts remain unknown;
`effectiveTotal` is available only with complete final coverage. Otherwise the
UI shows a known subtotal, valued attempts and unpriced attempts. No valued
scopes means unavailable, never zero. The current tool sources do not establish
invoice-backed actual account spend.

Model pricing requires an explicitly **served** model, usage time, matching
pricing context, region, modality, service tier and applicable cache/fee rules.
Every catalog rule retains its official source URL, review time and effective
interval in the immutable content digest. Old unversioned catalogs without
these fields are rejected and must be reviewed again. No provider model tariff
is enabled merely from a requested or selected model. Existing Codex, Cursor
and Antigravity evidence therefore remains unpriced when it lacks these facts.
A2A has no accounting extension, metadata fallback or dollar valuation.

### Kiro credit replacement

The adapter accepts `params.creditPricingContext: "commercial-paid-usd"` only
when the operator has verified that the standard commercial paid-plan USD
add-on tariff applies. Omit this parameter for unknown account/region terms,
free/trial eligibility, contracts or GovCloud. Unknown contexts retain collected
credits without a USD estimate. This is an applicability assertion, not a way
to choose a cheaper tariff. The adapter does not inspect private billing stores.

The reviewed 2026-11-12 snapshot in
`muxlio-adapters/cmd/agent-kiro/accounting.go` pins the published **$0.04/credit**
replacement rate and source URL to a SHA-256 catalog identity. Its applicability
starts at the review date; it does not reprice earlier usage. The source is
[Kiro pricing](https://kiro.dev/pricing/); see also
[add-on billing](https://kiro.dev/docs/billing/add-on-credits/).
Retain this tariff revision when introducing a successor. There are no runtime
price fetches. Use a new reviewed effective interval when pricing changes.

Only explicitly identified Kiro turns are priceable. Replayed completion echoes
are counted once, credits are never converted to tokens, and the reported model
multiplier is never reapplied. The calculation uses exact decimal arithmetic and
rounds once per turn, half to even, at 18 fractional USD digits. For example,
`0.18065595854063016 × 0.04` persists as `0.007226238341625206`.
A positive result below representable precision or an overflow leaves valuation
unavailable while preserving collected credits. Replacement amounts use cost
basis `credit-replacement` and an estimated reading with method, version and
catalog ID. They are never recorded as collected dollars or incremental spend.

Source coverage remains partial pending controlled validation of all supported
CLI versions and billing modes. A terminal event or successful process exit does
not prove complete economic coverage. Claude terminal quotes and owned ACP USD
session deltas retain an unspecified price basis as collected evidence; no local estimate replaces them.
Missing ACP baselines, interrupted quotes and unresolved routing remain
unavailable or partial. Kiro tokenizer proxies now fill only missing dimensions;
a directly reported token counter, including zero, has no redundant proxy.

Optional bounded scenarios, account ledger reconciliation and subscription
allocation are not enabled. They cannot contribute guessed values to canonical
reference subtotals.

For provider-specific source gates, live validation outcomes and the Cursor
ledger refresh command, see [USD cost recovery](usd-cost-recovery.md).
