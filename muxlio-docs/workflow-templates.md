# Reusable workflow templates

Codex, Kiro, Claude and other MCP clients can discover and instantiate the same
catalog without writing TypeScript or constructing a complete graph. Call
`list_workflow_templates` with `{}`, then `get_workflow_template` with a `name`
and explicit `version`. Both return parameter, run input and output JSON Schemas.
The catalog currently contains five shapes at version `"1"`.

A template is a versioned authoring recipe. Instantiation validates parameters
and compiles an ordinary Plan IR definition, including `inputSchema`,
`outputSchema`, `requiredExecutors` and its content digest. It performs no work
and stores nothing. `register_workflow_version` pins that compiled definition
as an immutable workflow version; `list_workflow_versions` lists those stored
definitions, independently of the template catalog. Changing executors or tasks
changes the plan digest. Reusing a compiled plan with different run inputs keeps
the same plan digest.

## Summarize and verify a museum guide

Inspect `get_workers` and choose installed executors. The following call to
`instantiate_workflow_template` uses illustrative capability names; replace them
with available executors. Authoring-client choice does not select an executor.

```json
{
  "name": "sequential-verification",
  "version": "1",
  "parameters": {
    "name": "exhibition-summary",
    "work": {
      "executor": "generic/writer",
      "task": "Summarize inputs.request as a short visitor guide. Return the summary on the result JSON output port."
    },
    "verify": {
      "executor": "generic/reviewer",
      "task": "Compare inputs.result with inputs.request. Return result as {approved:true} only if all dates and claims match; otherwise return {approved:false,feedback:...}."
    }
  }
}
```

The response has `{template:{name,version}, plan, digest}`. Inspect and edit
`plan` using the same tools as any authored Plan IR. If edited, remove the old
`digest` or recompute it (for example with the SDK's `computeDigest`). `build`
and `check_plan` validate JSON plans; registration computes the immutable version's digest.
Use `preflight_run` and `start_run` with the returned `plan` and separate input:

```json
{"request":{"title":"Coastal Stories","dates":"5–20 June","audience":"Families","source":"The exhibition contains paintings and recorded oral histories."}}
```

This input can be replaced with another guide without reinstantiating. The run
output is `{result: <summary>, verification: {approved:true, feedback?: ...}}`.
False, missing or malformed approval fails the verification output contract.
Executors still determine whether statements are factually correct; templates
enforce the declared result contract and dependency structure.

## Shapes and contracts

All parameter objects may include `name` to name the compiled workflow. Unknown
parameter fields are rejected. Required bounds are integers from 1 to 10000.

| Template | Required parameters | Run input | Run output |
| --- | --- | --- | --- |
| `sequential-verification` | `work`, `verify` | `{request: any}` | `{result: any, verification: {approved: true, feedback?: any}}` |
| `bounded-parallel-map` | `work`, `concurrency`, `maxItems` | `{items: [{id: string, value: any}]}` | `[{id: string, result: any}]` in input order |
| `review-repair-loop` | `repair`, `review`, `maxIterations` | `{request: any, initial: any}` | `{result: any, review: {approved: true, feedback?: any}}` |
| `external-approval` | `work`, `requestName`, `requestSchema` | `{request: any}` | Work's `result` value |
| `command-pipeline` | `steps` (1–100 steps) | `{request: any}` | Last step's `result` value |

Each work, repair or pipeline step has `executor` (required), optional `task`,
opaque executor `params`, and optional `output: {mime?, path?, schema?}`. It has
one output port named `result`, defaulting to inline JSON with schema `{}`.
Set `output.schema` to constrain JSON results, or `output.path` and `mime` for a
file artifact. Files remain artifact references, not decoded JSON values.
The catalog's `any` output positions accommodate either; the actual step's
output declaration remains visible in the compiled plan. Verification and review
accept `executor`, `task`, and `params` only; their inline JSON result schema is
fixed to `{approved: boolean, feedback?: any}`. Verification requires true.

- Sequential work receives `request`; verification receives `request` and `result`.
- Map work receives `item` as `{id,value}`. IDs must be nonempty and unique.
  Empty collections succeed. `maxItems` is included in the compiled input schema
  and map bound; `concurrency` bounds simultaneous item processing. A failed item
  fails the run.
- Repair receives `request` and `previous: {result, review}`. The first previous
  result is `initial`, with review `{approved:false}`. Each iteration repairs,
  then reviews with `request` and `result`; feedback and result carry into the
  next repair. Approval ends the loop, including on the final allowed iteration.
  Exhaustion fails the run.
- External approval uses the existing durable input-request wait. Work receives
  `request` and `approval` only after an accepted response. Optional parameters
  `requestExpiresSeconds` (positive, at most one year) and `requestPermissions`
  use existing expiration and responder authorization. For approval, set
  `requestSchema: {"const":true}`. A permissive schema also supports non-approval
  waits, such as collecting a delivery address. Inspect `get_input_requests` and
  respond with `respond_input_request`. No worker slot is held by the wait.
- Pipeline steps receive `request`, and steps after the first also receive
  `previous`, the preceding result. Dependencies are data bindings. Command
  program/argv belong in `params` for an installed command executor. Templates
  do not interpolate inputs into argv or add a shell. For example, a data-export
  step might use `params: {"program":"survey-export","args":["--format","csv"]}`
  and `output: {"mime":"text/csv","path":"survey.csv"}`. The command must follow
  its adapter's output-file contract; later steps receive the artifact reference.

All catalog tools run offline without Node, a repository checkout, or trusted
source execution. The existing `workflow_template` tool continues to return its
`echo` and `task` starter plans. For arbitrary shapes, author Plan IR or use the
SDK. Template results work with the existing JSON build/test tools, registration,
preflight, run inspection and UI; no template-specific runtime is involved.
