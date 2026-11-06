# Pinned environment manifests

Reproducibility records the software and permissions used for a plan. It does
not promise deterministic model output. Existing unpinned plans continue to use
the baseline executor contract.

An execute node can set `environment` to a version 1 environment manifest. The
same manifest must be present in the worker's adapter descriptor
(`<binary>.json`, under `environment`). The manifest is part of the plan digest
and survives SDK builder, symbolic task, async compiler, durable request, and
adapter wire serialization.

The closed metadata schema contains:

| Field | Meaning |
| --- | --- |
| `version` | Schema version, currently `"1"` |
| `digest` | SHA-256 of the manifest with `digest` set to the empty string |
| `protocolDigest` | SHA-256 of the shared typed adapter descriptor with `environment` omitted |
| `runners` | Runner names to exact normalized versions; built-in CLI adapters use the name `runner` |
| `packages`, `containers`, `skills` | Operator-managed names to SHA-256 content digests |
| `allowedEnvironment` | Environment variable names permitted at execution, never values |
| `permissions` | Declared `host-execution`, `network`, `workspace-read`, or `workspace-write` permissions |

Hashes use Go `encoding/json` on the shared contract structs (field declaration
order, sorted map keys, omitted empty optional collections), prefixed with
`sha256:`. Array order is significant. Use the shared Go helpers to produce
manifests rather than hashing arbitrary descriptor JSON formatting:

```go
// descriptor is the adapter's public descriptor, before attaching environment.
environment := &contract.EnvironmentManifest{
    Version: "1",
    ProtocolDigest: descriptor.ProtocolDigest(),
    Runners: map[string]string{"runner": "1.2.3"},
    AllowedEnvironment: []string{"PATH", "HOME", "TMPDIR", "LANG"},
    Permissions: []string{"host-execution", "workspace-write"},
}
environment.Digest = environment.ComputeDigest()
descriptor.Environment = environment
// Serialize descriptor beside the adapter; copy environment into execute nodes.
```

Operators install and verify packages, images, skills, and adapter descriptors
before advertising them. These digests are operator attestations, not a package
installer or distribution system. Changing software, skill contents, or allowed
permissions requires updating the manifest and recompiling the pinned plan.
Runner pins preserve prerelease and build suffixes (for example,
`1.2.3-beta+build.1`); probes reject unsupported version tokens rather than
matching a shorter prefix. Exact environment digests must match; there is no inferred version compatibility
or automatic fallback to a differently provisioned runner.

Before assignment, the shared dispatch matcher verifies the descriptor and
manifest digests, required capabilities, and observed runner versions. A worker
missing any pinned metadata is ineligible. The worker repeats readiness and
runner checks before starting pinned work, and CLI adapters re-observe their
own resolved runner before generation. Protocol parsing and CLI resolution stay
inside adapters. A version change after worker registration can therefore
invalidate an otherwise previously compatible plan. Probe results expose only
normalized version strings, never raw CLI diagnostics.

The manifest's environment allowlist filters worker-provided variables before
launch, including `PATH`, `HOME`, and runner resolution overrides. Include every
variable needed by the operator-managed installation. Internal log transport is
injected separately. Permissions record the declared environment and do not
grant access: existing worker authorization, execution policy, secret handles,
and isolation enforcement still apply.

Secrets remain in operator configuration and transient execution envelopes.
Exported manifests contain only names, normalized versions, digests, and known
permission names. Unknown manifest fields, variable values, and malformed pins
are rejected by authoring validation and the shared Go contract. Use `secretRefs`
and execution policy for secret delivery; never put credentials in manifests.
