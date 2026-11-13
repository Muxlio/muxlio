# Execution credentials and trust boundaries

Commands and agents execute arbitrary code. Native agent adapters require an
explicit operator authorization policy, described below. Other executors without
security declarations retain trusted native execution: host network, host
filesystem and the configured agent HOME. Worker logs identify native versus isolated execution.
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
`workspaceMode: "shared-write"`. Shared writes acquire an implicit exclusive
workspace reservation through durable coordinator admission; explicit concurrency
keys can additionally protect external resources. An omitted
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
    {"tokenFile":"submitter.token","permissions":["submit"]},
    {"tokenFile":"controller.token","runId":"run-a","permissions":["control"]},
    {"tokenFile":"worker-a.token","workerId":"worker-a","permissions":["worker","artifacts:read","artifacts:write"],"artifactDigests":["sha256:..."]}
  ]
}
```

Credential files are relative to the configuration directory or absolute. Keep
them protected and deliver their values as bearer tokens over TLS outside trusted
local networks. Worker credentials and scoped grants reload on every API request; replacing token
files or removing/revoking grants affects subsequent requests. Restart workers with
new credentials after rotation. The administrator token is fixed at server startup.
No scoped credential may list all runs or access another run. `submit` separately
authorizes creating runs and submission preflight; it does not grant read or
control access to the resulting run. Provision a run-scoped grant separately
when those operations are needed.
`read`, `control` (pause/resume/cancel/retry), `signals`, and `steer` are independent
permissions. `steer` authorizes session input delivery at
`/v1/runs/{runId}/sessions/{sessionId}/inputs`, subject to queue permissions and
advertised adapter input capabilities. Worker registration and protocol URLs must match the
grant's worker ID. Artifact permissions require an explicit digest allowlist;
read permission cannot request a PUT transfer plan. Empty scopes grant nothing.
Unauthenticated requests from remote addresses fail closed even when no tokens
are configured. Health checks remain public. `GET /v1/auth` checks a credential's
current validity without granting resource access; it returns 204 for an active
identity and denies revoked, expired, invalid or missing credentials.

### Remote web and MCP access

Keep the default loopback bind and use SSH forwarding for the easiest remote
setup (`ssh -N -L 17600:127.0.0.1:7600 host`, then open
`http://127.0.0.1:17600/`). Loopback UI requests receive automatic operator
credentials. Same-origin UI requests from private-network peers also receive
them when the Host is a private IP literal, such as `192.168.1.1`; this supports
trusted LAN access while preventing public DNS names from rebinding to the
coordinator. Cross-origin and cross-site browser requests are rejected, including
requests with explicit bearer credentials. Forwarded headers do not establish
trust.
Explicit credentials retain their scopes even on loopback. Empty, malformed, or
duplicate Authorization headers are rejected instead of falling back to local
operator access.

For an explicit remote deployment, use HTTPS with a valid certificate, separate
client grants, and an authenticated gateway for browser access. The UI has no
browser login form: the gateway must authenticate each user and attach that
user's scoped bearer credential to API requests. Validate the external browser
Origin at the gateway; preserve the original Host and scheme through an HTTPS
upstream, or remove Origin only after validating it when proxying to a different
upstream origin. Do not forward unauthenticated requests to a loopback upstream
with a rewritten loopback Host. Do not use a shared administrator token for all
remote users. Binding HTTP publicly does not authenticate anyone. Public HTTP
exposes bearer credentials in transit; use TLS or an SSH tunnel.

MCP HTTP accepts coordinator client grants directly as bearer credentials and
forwards each client's identity independently. Coordinator authorization remains
authoritative for each operation; tool annotations are advisory metadata only.
The separately configured listener credential is a trusted gateway credential
using the configured upstream identity, so protect it like that identity. File-based
`build` and `test` are restricted to local stdio MCP because they access the MCP
host outside coordinator authorization. HTTP clients can validate inline JSON
using `check_plan` without reading files or executing source on the MCP host.

### Advertising available isolation backends

Enable backends on each execution host in its operator-owned `config.json`:

```json
{"isolationBackends": ["bwrap"]}
```

Use `oci` on Linux for Podman/Docker, or `docker-vm` on macOS for Docker Desktop.
Both embedded and standalone workers verify the platform and launcher before
advertising `muxlio.dev/isolation/<backend>=true`. Missing launchers fail startup;
execution still validates the daemon, image and boundaries for each attempt.
VM tasks also require the existing task-level `enabled: true` opt-in. Installing
a launcher alone does not enable a backend.

## Unattended native agent authorization

`executors.<capability>.authorization` in the worker's local operator config is
independent of the submitting client's API credential and task `executionPolicy`.
The worker copies this typed policy into the adapter request immediately before
launch. Coordinator state, workflow task text, inputs, params, model output and
client setup never grant or replace it. Client permission to submit work and the
worker's permission to perform it are separate grants. Direct adapter callers
are trusted execution hosts and must protect their stdin envelope accordingly.
The existing executor `allow` host-execution gate remains an independent check.

`muxlio init` supplies all-allowed operator policies for bundled executors by
default: `allow: true`, trusted mode, all tools and commands, workspace writes,
network access, and native isolation. Operators can restrict individual executors
in `config.json`. Repeated init fills missing settings while preserving explicit
policies, disabled executors, and null policies. `--allow-command` remains an
explicit way to re-enable command execution after it has been disabled.

Outside those initialized defaults, missing authorization denies generation in
all five bundled native agents. A
restricted policy defaults to no tools or commands and no writes. Both `network`
and `isolation` must be explicit. Tool names are generic: `read-file`,
`write-file`, `shell`, `network`, or `*`. `allowedCommands` describes allowed
commands; an empty list denies commands. A mapping must reject any command or
tool scope it cannot enforce; this implementation does not translate command
strings into permissive shell prefix patterns. Network `none` refers to tool
traffic; connecting to the model API is still required. `isolation: required`
requires an enforced worker backend even if a task requests trusted native
execution. It never silently falls back. Native CLI permissions alone are not
OS isolation, and cannot contain malicious runner software.

For explicitly trusted high-autonomy jobs, configure each capability separately:

```json
{
  "executors": {
    "agent/claude": {
      "allow": true,
      "authorization": {
        "mode": "trusted",
        "allowedTools": ["*"],
        "allowedCommands": ["*"],
        "workspaceWrite": true,
        "network": "allowed",
        "isolation": "native"
      }
    }
  }
}
```

Trusted mode requires all unrestricted grants explicitly. `compatibility` is an
operator-approved migration mode with the same explicit grants. For Codex it
retains `--approve-for-me` auto-review; trusted mode selects the explicit bypass
flag. Compatibility never restores automatic policy defaults, version fallback,
or broad shell environment inheritance. Codex uses
`shell_environment_policy.inherit=none` in both modes. Supply necessary tool
credentials through the existing explicit secret contract, rather than ambient
shell inheritance. For the other native agents compatibility uses the same
explicit bypass mapping as trusted mode. These modes trust workspace hooks,
plugins and tools with the execution host's accessible credentials and files.

Current adapter argument mappings accept these exact builds:

| Native agent | Runner versions | Restricted policy | Trusted / compatibility |
| --- | --- | --- | --- |
| Claude | 2.1.319 | Restricted and safe modes, empty MCP configuration, explicit file tools, `dontAsk`, no permission prompts; no shell/network tools | Explicit bypass permission mode |
| Codex | 0.167.0, 0.167.2 | Rejected: full tool/MCP/hook enforcement is unqualified | Bypass / automatic approval review |
| Cursor | 2026.11.05-86047df, 2026.11.11-b912975 | Rejected: full restrictive enforcement is unqualified | Explicit run-all and workspace trust |
| Kiro | 2.37.0 | Rejected: selective tool trust does not prove workspace hook or network containment | Explicit all-tools and workspace trust |
| Antigravity | 1.5.5, 1.5.6, 1.5.8 | Rejected: sandbox/plan flags do not prove the full contract | Explicit skip-permissions |

For Claude file-only execution, use `mode: restricted`, `allowedTools:
["read-file", "write-file"]`, empty `allowedCommands`, `workspaceWrite: true`,
`network: none`, and an explicit isolation choice. File tools are confined by
restricted mode to the working roots, including the declared output scratch.
No tool is exposed by default. An unknown mode, field, build, unavailable
version probe or unsupported restriction fails before generation. CLI upgrades
require updating the adapter-owned mapping and fixtures and rerunning live
qualification; shared worker and coordinator code must never select CLI flags.
The policy log records accepted and denied attempts with a valid policy (including
missing-policy denials). It records the SHA-256 digest, mode, tool/command counts, writes,
network and isolation requirements, without command strings or environment
values. Registration readiness probes remain read-only and do not qualify
permission enforcement.

Run the deterministic adapter, worker and cross-adapter conformance tests first.
Live qualification is opt-in because it starts billable generations with the
operator's existing login:

```sh
cd muxlio-adapters
MUXLIO_LIVE_AUTHORIZATION=all go test ./tests/conformance -run '^TestNativeAuthorizationLive$' -v -count=1
# Or select one or more agents, e.g. MUXLIO_LIVE_AUTHORIZATION=claude,codex
```

The probe verifies an edit and shell content test under the trusted policy and a
denied shell operation under restricted policy, including prompt-based elevation
attempts. Claude also must copy an unpredictable fixture through its restricted
file tools; trusted-mode success alone does not qualify those grants.
Unsupported restrictive mappings must reject before generation. All
runs are noninteractive with deadlines and Unix process-group cleanup of the
adapter, runner and child tools. Explicit live selection on other platforms fails
until equivalent process containment is implemented. An explicitly selected missing CLI,
authentication failure, backend failure or timeout fails the probe; it is never
counted as successful denial. Run live qualification on the actual execution host before enabling a mapping;
argument fixtures alone do not prove successful live enforcement. ACP/A2A
transport adapters retain their separate protocol trust contracts and are not
native CLI mappings.

## Workflow compilation

The recommended validation path for untrusted or agent-generated submissions is
JSON Plan IR: use MCP `check_plan` (an in-memory JSON document), or `build`/`test`
with a `.json` file. These paths never execute TypeScript, JavaScript, imports,
package scripts, or compiler hooks. Catalog templates also produce JSON without
Node. Do not enable source compilation merely to validate generated source.

Loading `.ts`, `.js`, or `.mjs` executes arbitrary code before Plan IR validation.
This includes CLI `build`, `check`, `test`, and `run`, and MCP `build`/`test`.
The operator must explicitly set `MUXLIO_TRUST_WORKFLOW_CODE=1`; MCP additionally
requires `trustedSource: true` on each source call. The tool descriptions and
rejection messages expose the risk before execution. This acknowledgement is
not a sandbox or a substitute for reviewing the source and dependencies.

Compilation invokes only project-local tsx **4.23.15** using Node, checks its
package version, and never invokes npx or installs packages. Install the root
lockfile explicitly with `npm ci` in a trusted environment. The version check is
not an integrity check against malicious local dependencies: the workflow,
Node binary, and installed dependency tree must all be trusted.

The compiler receives an allowlisted environment (PATH, a disposable HOME and
TMPDIR, MUXLIO_COMPILE, a fixed NODE_OPTIONS heap limit, and disabled tsx caching).
Operator environment tokens and Node injection hooks are not forwarded.
Compilation stops after 30 seconds, 8 MiB of stdout, or 64 KiB of stderr.
Node's V8 heap is capped at 256 MiB; this is not a total process-tree memory cap.
On Unix, cancellation and cleanup after compiler exit kill the compiler process
group, including remaining descendants; on other platforms cancellation kills
the direct child and bounds pipe draining. These are resource safeguards
for trusted code, not protection from deliberately hostile code.

Source compilation still has host filesystem and network access. A disposable
HOME does not prevent reading credentials by absolute path. Worker isolation
policies in `internal/sandbox` apply to worker execution, not authoring, and
must not be presented as compiler isolation. If source compilation is necessary,
run the compiler on a separate disposable host/container under a dedicated
identity, with only the reviewed workspace and dependencies mounted, no
operator credentials, network denied, and OS memory/process limits. Transfer
only the resulting JSON Plan IR to the operator's MCP host for `check_plan` and
submission. Never enable trusted source loading on a host that handles
unreviewed definitions.
