# Execution credentials and trust boundaries

Commands and agents execute arbitrary code. Existing requests without security
declarations retain trusted native execution: host network, host filesystem and
the configured agent HOME. Worker logs identify native versus isolated execution.
Native execution is not a filesystem sandbox. Adapters that require host execution
also require the operator's existing executor `allow` permission.

Declare restrictions in execute `params`, for example:

```json
{
  "executionPolicy": {
    "required": true,
    "allowedCapabilities": ["command"],
    "allowedSecrets": ["service"],
    "trust": "restricted",
    "network": "none",
    "filesystem": "scratch"
  },
  "secretRefs": {"service": "service-production"},
  "isolation": {"mode": "required", "runtime": "bwrap"}
}
```

Restricted execution requires an enforced isolation driver. Missing drivers,
unsupported platforms and native fallbacks fail closed. The current Linux bwrap
and OCI drivers disable network access and expose writable attempt scratch.
Host workspace access must be declared separately using `workspace-read-only`
with `workspaceMode: "read-only"`, or `workspace-write` with
`workspaceMode: "shared-write"` and exclusive concurrency keys. An omitted
filesystem declaration on a restricted attempt grants scratch access only.
OCI also requires an image; the macOS VM driver is not implemented and refuses
execution. These are process boundaries, not interception of agent tool calls.
They do not constrain execution performed by a remote provider's service.

The operator configures each worker executor with handle-to-environment-name
mappings, for example:

```json
{"executors":{"command":{"secretEnv":{"service-production":"SERVICE_API_KEY"}}}}
```

Only declared, allowlisted names resolve, immediately before execution on that
worker. A handle cannot read arbitrary environment variables. Missing or denied
handles fail without disclosing their values. Retries resolve handles again.
The adapter receives resolved values through its existing stdin secret contract;
the durable assignment contains handles only. Transient local `Secrets` values
are excluded from JSON and refused by durable coordinator dispatch.

Execution parameters containing conventional credential fields (such as
`secrets`, `password`, `API_KEY` or `authorization`) are rejected before a plan is
pinned. Do not embed credentials in task prose, source code, bindings, handles,
run inputs or arbitrary parameters: arbitrary strings cannot reliably be
identified as credentials. Worker result fields and progress logs redact known
execution credentials, including secret-bearing configured environment values.
Artifact publication rejects files containing those values, and snapshots skip
them. Redaction does not protect against malicious code that encodes or sends
credentials elsewhere; give only necessary credentials to trusted code.

## Scoped API credentials

The administrator token authorizes the complete control plane. The existing
cluster worker token trusts its holder with all worker identities and artifact
transfer. Use separate scoped credentials for remote workers or clients that
must not have those powers. Set `accessGrants` in the coordinator configuration:

```json
{
  "accessGrants": [
    {"tokenFile":"client-a.token","runId":"run-a","permissions":["read","signals"]},
    {"tokenFile":"worker-a.token","workerId":"worker-a","permissions":["worker","artifacts:read","artifacts:write"],"artifactDigests":["sha256:..."]}
  ]
}
```

Credential files are relative to the configuration directory or absolute. Keep
them protected and deliver their values as bearer tokens over TLS outside trusted
local networks. Grants load at server startup; restart to revoke or rotate them.
No scoped credential may list all runs, create runs or access another run.
`read`, `control` (pause/resume/cancel/retry), `signals`, and `steer` are independent
permissions. `steer` is reserved for session steering routes; it does not itself
add a steering endpoint. Worker registration and protocol URLs must match the
grant's worker ID. Artifact permissions require an explicit digest allowlist;
read permission cannot request a PUT transfer plan. Empty scopes grant nothing.
Unauthenticated requests from remote addresses fail closed even when no tokens
are configured. Health checks remain public.

The embedded and standalone UI proxies preserve explicit bearer credentials,
including scoped client and worker credentials. They supply the operator token
implicitly only for local peers using a loopback/localhost Host and same-origin
browser requests. Remote UI API requests must supply their own credentials;
cross-origin requests cannot borrow the local UI's operator authority. Forwarded
address headers do not establish local trust. A local reverse proxy is itself a
trusted operator component and must preserve the original Host and enforce its
own client authentication.
