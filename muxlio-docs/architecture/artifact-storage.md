# Artifact storage configuration

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
