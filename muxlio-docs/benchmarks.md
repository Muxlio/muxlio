# Reference workflow benchmarks

The open [reference suite](../muxlio-core/benchmarks/) runs incident processing
workflows through the real Plan interpreter, SQLite store and executor contract.
It retains individual observations and execution evidence. It is a reproducibility
tool, not evidence that one provider or orchestration system is faster than another.
The command cases are small functional baselines; they do not represent model
latency, production scale, distributed dispatch or provider quality.

| Case | Work and independent correctness check |
| --- | --- |
| `sequential` | Count 12 incident records, then validate the severity report in a dependent process. |
| `parallel-map` | Parse each incident in its own process with bounded map concurrency; verify every field and output order. |
| `mixed-provider` | One configured agent writes a severity report, a second independently reviews it, then a command checks exact counts. This is the non-coding agent case. |
| `large-artifact` | Export a 2 MiB incident file, transport it through a file dependency and independently check its size and SHA-256. |
| `cancellation` | Cancel at a durable wait after triage; verify the cancelled state and that the downstream report never executes. |
| `recovery` | Stop cooperatively at that wait, close/reopen SQLite, call `RecoverRuns`, signal and finish. Verify that the completed attempt is preserved unchanged, not replayed, and that the final report is correct. |
| `coding` | An agent implements an incident parser. A separate verifier checks unseen inputs, trimming, zero counts, malformed rows, duplicate IDs and input immutability. |
| `agent-authored` | An agent writes a bounded map/project Plan IR workflow. Execute that generated workflow on the corpus, an empty array and an unseen record, checking exact output preservation. Retain generated source and verification traces. |

The inspectable corpus, prompts, verifiers and plan constructors are in
[workloads.go](../muxlio-core/benchmarks/workloads.go). Agent workflow generation
uses an explicit IR recipe; it measures following and executing that recipe,
not autonomous workflow design. Its verifier accepts only bounded structural
nodes, rejects recursion and imposes a deadline.

## Run and preserve raw data

Prerequisites: the Go version in `muxlio-core/go.mod`, Node.js with `node:test`
support, Git, and installed executor binaries with their `.json` manifests.
Build a command-only adapter directory as follows, replacing `/absolute/scratch`
with a writable directory outside the checkout:

```sh
mkdir -p /absolute/scratch/adapters
cd muxlio-adapters
go build -o /absolute/scratch/adapters/command ./cmd/command
cp cmd/command/adapter.json /absolute/scratch/adapters/command.json
cd ../muxlio-core
go test ./benchmarks
MUXLIO_BENCHMARK_ADAPTERS=/absolute/scratch/adapters go test ./benchmarks -count=1
node --test benchmarks/compare.test.mjs
go run ./benchmarks --adapters /absolute/scratch/adapters --out /absolute/scratch/capacity-1 --capacity 1 --concurrency 4 --repetitions 3
go run ./benchmarks --adapters /absolute/scratch/adapters --out /absolute/scratch/capacity-4 --capacity 4 --concurrency 4 --repetitions 3
node benchmarks/compare.mjs /absolute/scratch/capacity-1 /absolute/scratch/capacity-4 > /absolute/scratch/comparison.json
```

Each output directory must be new; reruns cannot overwrite evidence. The default
cases are the five command-only cases. All eight cases have plan validation tests.
The opt-in integration test also tests the agent paths using clearly identified
contract fixtures. Those fixtures are not model measurements or agent provenance.

For real agents, install and authenticate the desired adapters using the normal
adapter setup. Select cases explicitly. For example, from `muxlio-core`:

```sh
go run ./benchmarks --adapters /absolute/installed/adapters --out /absolute/scratch/agents --cases mixed-provider,coding,agent-authored --primary agent/codex --secondary agent/claude --metadata /absolute/scratch/metadata.json --repetitions 3
```

The example executor IDs are configuration choices. Different executor IDs do
not prove different underlying providers: record the actual providers and models.
Supply a metadata JSON object with these nonempty string fields:

```json
{
  "models": "Actual provider/model IDs for each executor, reasoning settings and context limits",
  "versions": "Agent CLI versions and configuration, including automatic model selection",
  "pricingBasis": "Dated USD prices or unavailable; include subscription and host cost treatment",
  "machine": "CPU model, RAM, OS, power mode, local/remote placement and network conditions",
  "notes": "Cache state, authentication setup without secrets, background load and run ordering"
}
```

Agent runs execute generated code using the configured host executors. Use a
disposable workspace and suitable credentials. Review the raw bundle before
sharing: inputs, model responses, files and local environment details can be
sensitive. This suite's published corpus is synthetic and contains no credentials.

## What to publish

Publish the complete output directories alongside the comparison JSON, the
source revision and the exact source snapshot if the checkout was dirty. A
summary alone is insufficient. The runner captures:

- `manifest.json`: invocation, revision, Git status, environment, concurrency,
  metadata, adapter binary hashes and adapter manifests.
- `sources.json` and `corpus.json`: runner/workload source bytes and input corpus;
  source hashes also appear in the manifest. These two sources are not a snapshot
  of the entire runtime; preserve the full checkout for a dirty revision.
- Per case/repetition: exact `plan.json`, `input.json`, `observation.json`,
  `events.json`, `activations.json`, `attempts.json`, the SQLite database and
  executor work directories with output artifacts and logs.
- Recovery: pre-recovery attempts; agent authorship: generated plan plus input,
  run and events for each independent verification execution.

Failures return a nonzero exit code, retain observations once a run exists and
do not stop subsequent cases. Setup failures may leave an incomplete directory;
publish that fact and keep missing repetitions visible. No real-provider results
are shipped or claimed by this suite.

## Interpret the measurements

Wall time starts before per-case database setup and ends after execution and
correctness checking, including recovery and authored-workflow validation. It
excludes final raw-data serialization. Each case has a two-minute engine timeout;
executor calls have a 90-second timeout. Agent workflow verification has a
five-second deadline per verification run.

Queue delay measures local admission from `Execute` entry to acquiring a slot.
It excludes dependency waiting, external provider queues and network dispatch.
Every admission has timestamps and delay; a cancelled admission records its
terminal wait timestamp in `startedAt` with `cancelled: true` and is counted
separately from started executions by the comparison tool.

Recovery correctness and cancellation correctness are booleans when their
checks are reached, and null when inapplicable or not reached due to failure.
Recovery covers a cooperative shutdown at
a persisted wait, not a process kill, host failure or ambiguous remote operation.
Cancellation covers a wait boundary, not cancellation of an in-flight provider.
Manual interventions count operator actions (zero for the automated harness);
`manualInterventionRequired` separately exposes unresolved reconciliation. If an
operator intervenes, preserve the original data and disclose actions and elapsed
time alongside the results; do not silently turn an assisted run into an automatic
success.

Cost uses only adapter-reported observed USD or explicitly estimated USD,
separately. Null means unavailable; zero means reported zero. Unknown attempts
remain counted. Partial subtotals are not total cost. Command-only runs have
unavailable model cost, not free execution. Tokens never come from credits or
context-window estimates. Subscription, machine and operator costs require
separate disclosure.

The comparison tool retains each configuration's manifest, failures, missing
samples, raw wall times, recovery results, interventions and cost availability.
Median and nearest-rank p95 wall times use only correct samples; admission queue
statistics include started executions from failed runs too. Three samples are
useful for a smoke run, not a reliable tail-latency estimate. Use more repetitions,
alternate configuration order, and disclose warmups without discarding failures.
Compare the same workload and verification policy, disclose all changed settings,
and report quality and completion rates beside timing. Provider/model, hardware,
cache, concurrency and fault differences prevent unsupported apples-to-apples
claims. The tool deliberately computes no ranking or speedup claim.
