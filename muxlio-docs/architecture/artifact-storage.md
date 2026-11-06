# Artifact storage configuration

Remote artifact transfers are limited to 256 MiB per object. Uploads are staged
and checked against their SHA-256 digest and declared nonzero size before sending;
the coordinator's proxy also bounds request bodies and verifies content before
committing it to the CAS. Interrupted uploads do not publish partial objects and
can be retried under the same digest.

Downloads stage bytes privately and publish to the destination only after digest
and length verification. A transfer makes at most three attempts, retaining the
contiguous prefix and requesting `Range: bytes=<offset>-` after interruption.
The proxy supports these ranges, and direct storage must return a matching
`Content-Range` for a partial response. Servers ignoring ranges cause a bounded
restart. Exhausted retries return an `IncompleteTransfer` with the expected
digest, received prefix length, expected size (zero when unknown), and cause.
Temporary prefixes are removed when the call returns; resumption occurs within
the call, not across worker process restarts. A default HTTP attempt times out
after 30 seconds. Transfer plans must match the requested digest and method.
Direct plans also carry the headers required by the storage signature; clients
apply those headers to every request, including resumed downloads.

The coordinator uses its local content-addressed artifact directory by default.
To enable direct S3-compatible object transfers, set these environment variables
before starting `muxlio server start`:

```sh
export MUXLIO_S3_ENDPOINT=https://objects.example.test
export MUXLIO_S3_BUCKET=muxlio-artifacts
export MUXLIO_S3_ACCESS_KEY=...
export MUXLIO_S3_SECRET_KEY=...
export MUXLIO_S3_REGION=us-east-1 # optional; defaults to us-east-1
export MUXLIO_S3_SESSION_TOKEN=... # optional
export MUXLIO_S3_THRESHOLD_BYTES=5242880 # optional; defaults to 5 MiB
muxlio server start
```

Transfers at or above the threshold are written to S3 using path-style requests
and AWS Signature Version 4. Small artifacts remain in the local CAS. If an S3
upload fails, the artifact is saved locally; reads also fall back to local
storage. Consumers verify SHA-256 before bytes are returned. Keep credentials
in the process environment or a secret manager; they are not printed by the
artifact driver. S3-compatible services must support SigV4 and path-style
bucket access.

## Live progress and intermediate snapshots

Workers batch live text at 250 ms intervals (or 64 KiB), retaining at most
1 MiB per attempt. The coordinator atomically stores `execution.progress`
events with run sequence numbers and attempt/assignment provenance. Reconnect
with `GET /v1/runs/<id>/events?after=<last-seq>` or the SSE last-event ID;
`muxlio events <id> --since <seq>` and MCP `get_events` expose the same history.
Ingestion accepts at most 256 progress events and 1 MiB of text per attempt,
independently of worker buffering, and deduplicates retried source sequences
within their worker generation and coordinator epoch.

During execution, declared output files are sampled once per second and at
executor return. Changed regular files up to 4 MiB become immutable SHA-256
objects before their `artifact.snapshot` event is accepted. Each attempt is
limited to 128 observations and 64 MiB of snapshot uploads; ingestion enforces
the same snapshot event and byte budgets. Missing files and
unavailable transfers can be observed on a subsequent sample. A snapshot is an
observation of bytes at that point, not a declaration that the file is complete.
The coordinator verifies stored content and size before recording provenance;
reads verify the digest even after the worker's scratch directory is lost.

Inspect history with `muxlio snapshots <id> --json`, or select the latest
observation per activation/output using `--current`. `--attempt <id>` and
`--activation <id>` filter provenance; `--since` and `--limit` page history.
The API is `GET /v1/runs/<id>/snapshots`, with `current`, `attemptId`,
`activationId`, `after`, and `limit` query parameters. Responses contain
`snapshots`, `nextAfter`, and `hasMore`. MCP exposes this as `get_snapshots`;
use `read_artifact` with any recorded digest. The run inspector labels latest
and historical snapshots separately from outputs and opens their stored bytes.

Snapshot history survives failure and retries. It never updates named activation
outputs or downstream inputs. Final file references are uploaded on successful
executor return and named outputs are committed only after attempt validation
succeeds. Failed and cancelled attempts retain observations without publishing
them as successful final results.

## Retention and garbage collection

`muxlio gc` performs offline maintenance on a local runtime. Stop the coordinator
first; maintenance holds the same `service.lock` used by standalone and managed
servers. Running, waiting, paused, and other nonterminal runs remain protected
while the server is stopped. The command emits a JSON dry-run report by default,
`--dry-run` explicitly requests a preview. Reports list artifact digests and byte sizes, event and stdout/stderr row counts by
run, cached plan digests, and a review token. Apply requires that token and
recalculates reachability before deleting anything. If the candidate set changes,
review the new report and use its token.

```sh
muxlio gc --data-dir ~/.muxlio
muxlio gc --data-dir ~/.muxlio --apply <token-from-reviewed-report>
```

Default retention after a run becomes terminal is seven days for event history,
three days for stdout/stderr, and thirty days for artifacts. Unreferenced compiled
plan cache entries expire fourteen days after registration. Override these
independently with `--events 168h`, `--logs 72h`, `--artifacts 720h`, and
`--caches 336h`; use the same flags for preview and apply. Zero disables cleanup
for that category. Negative durations are rejected. Unreferenced local artifacts
also receive the artifact duration as a grace period after their file modification
time, protecting uploads that have not yet acquired a reference.

Reachability includes retained run inputs and outputs, activation inputs and
outputs, execution results, queued work and inputs, retained event references,
registered schedules and triggers, and pinned plans and their transitive
references. Snapshot observations survive event-history cleanup until artifact
retention expires. Shared content stays protected if any retained scope refers
to it. Event sequence numbers remain monotonic after history cleanup and retry.
Unfinished execution attempts, active or uncertain assignments, and effects
awaiting reconciliation protect their runs and fork ancestors even after a run
becomes terminal. Worker diagnostics and input schemas participate in marking;
pending, requeued, and dead-letter subscription deliveries retain their payload
references so delivery and operator requeue remain safe.
Unread events also remain protected until every enabled subscription's dispatcher
cursor passes them, preserving fanout and artifact references across long outages.
Approval tokens bind to log row identities and content as well as their counts,
so replacing logs between preview and apply requires another review.

Pins are durable and override retention:

```sh
muxlio gc --pin-run <run-id>
muxlio gc --pin-plan sha256:<plan-digest>
muxlio gc --pin-artifact sha256:<artifact-digest>
```

Use `--unpin-run`, `--unpin-plan`, or `--unpin-artifact` to remove a pin.
Artifact pinning verifies that the local bytes exist and pass integrity checks.
For an existing fork run, register its dependency before maintenance with
`muxlio gc --fork-run <child-id> --parent-run <parent-id>`. This records ancestry;
it does not create a fork. A retained descendant protects every ancestor's data.
Cycles and missing parents are rejected. `--fork-run <id> --retain-until
<RFC3339-time>` sets an explicit deadline for all enabled categories and preserves
any existing ancestry relationship.

Intentional artifact removal records a persistent expiration tombstone before
unlinking bytes. A later GET or HEAD returns HTTP 410 with
`MUXLIO_ARTIFACT_EXPIRED`; unknown digests return 404, and damaged bytes still
produce an integrity failure. Reuploading valid bytes restores the artifact.
Run and activation metadata remain available after payload cleanup.

The report identifies `artifactStorage: "local"`. GC sweeps the runtime's local
CAS and SQLite event/log/plan caches; it does not enumerate or delete remote S3
objects, worker workspaces, or transfer staging files. Remote bucket lifecycle
rules must preserve content referenced by runs and pins; a blanket age-based
bucket expiration cannot provide that guarantee.
