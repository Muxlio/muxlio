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
OCI also requires an operator-approved Linux image. macOS can opt into Docker
Desktop with `{"mode":"required","runtime":"docker-vm","enabled":true,
"image":"your-approved-image"}`. The legacy `macos-vm` runtime selects the same
backend. It explicitly uses the `desktop-linux` Docker context; missing Docker
Desktop, images or permissions fail execution without native fallback. There is
no bespoke hypervisor to install or maintain. These are process boundaries, not interception of agent tool calls.
They do not constrain execution performed by a remote provider's service.

The backend interface lives in `muxlio-core/internal/sandbox`; workers dispatch
it independently of agent adapters. Successful execution logs include structured
boundary capabilities:

| Backend | Network | Filesystem | Processes | Host credentials |
| --- | --- | --- | --- | --- |
| `native-trusted` | Host | Host | Host | Accessible through host files |
| `bwrap` (Linux) | None | Scratch, system binaries/libraries and declared mounts | Linux namespaces | Only explicitly supplied credentials |
| `oci` (Linux Podman/Docker) | None | Image, scratch and declared mounts | Linux namespaces | Only explicitly supplied credentials |
| `docker-vm` (macOS Docker Desktop) | None | Image, scratch and declared mounts | Namespaces inside a shared Linux VM | Only explicitly supplied credentials |

Docker Desktop containers share the VM kernel; they are not a dedicated microVM
per agent. Docker Sandboxes (`sbx`) is a separate product with its own workspace,
network and credential policies; it is not automatically selected or treated as
equivalent to an offline backend. New integrations must implement the backend
contract and pass conformance before operators advertise them.

Workspace capabilities distinguish `private-clone` (an independent regular-file
snapshot in scratch), `read-only-host`, and `writable-host`. A private clone
prevents accidental concurrent workspace edits from colliding but provides no
OS containment under native execution. Shared writes deliberately change the
host source and still require exclusive resource admission. Isolation does not
remove secrets already present in a supplied workspace, image or adapter bundle.
Containers run without Linux capabilities, with no-new-privileges, a process
limit and a read-only image filesystem; their guest HOME and TMPDIR are scratch.
Container cleanup removes the named container after execution or cancellation.
The container CLI retains the operator's host runtime configuration; it does not
inherit the guest's scratch HOME. The pinned environment allowlist filters values
passed to the guest, and ambient host environment variables are never implicitly
forwarded there. Images run the selected adapter as their explicit entrypoint. Pinned task
readiness probes run through the same backend and environment filter before
work starts; they do not execute natively when required isolation is unavailable.
Worker registration probes still inspect the operator-managed host environment.

Before advertising a backend, run its real boundary conformance on the worker:

```sh
cd muxlio-core
MUXLIO_SANDBOX_CONFORMANCE=bwrap go test ./internal/sandbox -run TestBackendConformance -v
MUXLIO_SANDBOX_CONFORMANCE=oci MUXLIO_SANDBOX_IMAGE=your-approved-image go test ./internal/sandbox -run TestBackendConformance -v
# macOS, with Docker Desktop running:
MUXLIO_SANDBOX_CONFORMANCE=docker-vm MUXLIO_SANDBOX_IMAGE=your-approved-image go test ./internal/sandbox -run TestBackendConformance -v
```

The image must already be available and match the host CPU architecture. Tests
compile a static Linux probe and exercise actual filesystem reads/writes, network
interfaces and host socket access, process visibility, credential inheritance,
read-only versus writable mounts, and simultaneous independent scratch trees.
An explicitly selected unavailable backend fails the test. Without explicit
selection, the suite tests installed Linux bubblewrap or skips with a reason;
a skipped test does not qualify a backend. Re-run after runtime/config changes.

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
