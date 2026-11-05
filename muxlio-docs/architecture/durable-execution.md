# Durable execution contracts

A workflow is a domain-neutral graph. Runs, activations, attempts, waits, and
remote operations describe execution, independent of an executor or provider.
Scheduling extensions must preserve these identities and recovery rules.

## Identity and immutable data

| Record | Identity and durable contract |
| --- | --- |
| Compiled plan version | SHA-256 digest of canonical Plan IR, excluding its digest field. Unknown IR fields participate in the digest. A registered digest cannot be overwritten with different bytes. |
| Run | Unique run ID, workflow name, pinned plan bytes and digest, immutable JSON input, state, output/error, creation/update times. A workflow name is a label, never a version lookup during recovery. |
| Activation | Logical node occurrence: run ID, structural `scopePath`, node ID, occurrence (currently zero), stable activation ID, resolved immutable input bindings, state, outputs/error. |
| Attempt | One executor invocation: attempt ID, run ID, activation ID, one-based `retryNumber`, executor, state, start/end times, failure and available usage/duration. |
| Transition | Append-only event: run ID, strictly increasing per-run `seq`, timestamp, type, subject ID, payload. Sequence, not timestamp, orders history. |

`scopePath` is the activation's parent structural scope. The empty path denotes
the root. Nested map keys, loop iterations, branches and child invocations extend
that path; two nodes with the same node ID in different scopes are different
logical occurrences. The current activation ID concatenates run, scope segments,
and node with colons; it is opaque to consumers. Retry numbers never enter the
activation identity. An assignment's generation is a fencing token, not a new
logical occurrence or retry number.

Creating a run validates and snapshots its compiled plan before execution. Editing
or registering a workflow changes only future runs. `UpdateRunPlan` and
`RepinRunPlan` accept only the existing pin; changing even a paused loop's budget
requires a new run. This deliberately removes the former plan-replacement behavior.
Recovery verifies both pinned content and the stored digest before interpreting.
Run input has no update operation. Activations snapshot resolved bindings before
execution; executor attempts reuse that snapshot, including across explicit retry.
Older activations without snapshots retain the legacy binding-resolution behavior.

## State transitions

The coordinator owns transitions; executor and worker lifecycle is separate.

| Entity | Lifecycle |
| --- | --- |
| Run | `created` → `running`; `running` ↔ `waiting`; `running` → `paused`; explicit resume returns `paused` to `running`; active runs finish as `succeeded`, `failed`, or `cancelled`. |
| Activation | `blocked` → `queued` → `running`; `running` ↔ `waiting`; execution finishes as `succeeded`, `failed`, or `cancelled`. The current sequential interpreter creates nodes directly as `queued`. |
| Attempt | `running` → `succeeded`, `failed`, or `cancelled`. A crash can leave the outcome uncertain; it does not prove failure or permit repeating effects. |

A succeeded run is terminal. Failed/cancelled runs require explicit retry.
Retry atomically requeues incomplete activations, clears their errors, preserves
all succeeded activations and outputs, and restores the same pinned plan. The next
executor invocation gets a new attempt ID and a higher number on the same logical
activation. Automatic retries follow the same identity rule. Successful activation
outputs are checkpoints: recovery walks reuse them without calling the executor.
Replaying traversal is not permission to replay external work.

SQLite stores current rows and their corresponding events transactionally. Events
also include activity and attempt updates, so not every sequence entry represents
a state change. Events support ordered observation and diagnostics; the current
engine recovers from stored rows, plan bytes, outputs, dispatches, and wakeups,
not by rerunning executor calls from the event log. Memory implements the same
API semantics but does not survive process exit.

## Waiting and remote operations

Waiting represents persisted continuation data, not ownership of a live process.
A timer has a stable ID, run/activation IDs, original absolute deadline, and
pending/fired/cancelled state. Replaying scheduling preserves the original deadline
and state. A fired timer remains readable until the activation checkpoints success;
losing a process between wakeup delivery and checkpointing must not restart time.
A signal is persisted with run, name, sequence, payload, timestamp and optional
idempotency key. A duplicate delivery key does not create another message.

The current synchronous interpreter sleeps or polls as a convenience while a
caller is connected. These goroutines are transient: no process has to remain
alive during the wait. A restarted coordinator reconstructs from the pinned plan,
activation checkpoints, timer deadline or stored signal. Signals currently retain
all deliveries and select the latest payload by name; they are not yet a
per-occurrence consuming mailbox. Scheduler extensions must define that matching
contract explicitly before adding consumption semantics.

Remote execution persists a dispatch/request and attempt identity before a worker
claims it. Assignments carry generations and leases; results are correlated to
the attempt and fenced assignment. Restart is not a timeout failure or a retry:
an unclaimed dispatch can be safely delivered, while a claimed dispatch with an
unknown outcome must await its result or reconciliation. A completed activation
must never be sent to a worker again merely because a worker or coordinator
restarts. Local executor calls do not acquire remote durability guarantees.

Structural evaluation and traversal are replayable from immutable values and
checkpoints. Executor calls and outbox deliveries can cause external effects.
Delivery is at least once, not exactly once: a lost acknowledgement can leave an
effect completed outside the coordinator without a local success checkpoint.
Automatic retries currently require the empty/default or `retryable` effect policy;
`idempotency-key`, `reconcile`, `manual`, and `none` do not grant automatic replay.
An explicit retry is an operator decision about incomplete work. Providers own
protocols, authentication, usage normalization and limitations; no provider name
changes these state or identity rules. Missing usage remains unavailable, distinct
from reported zero.
