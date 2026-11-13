# Installed-client acceptance

Unit tests (`npm test`, `go test ./...`) do not require a release or credentials.
The separate installed-client CI job builds the actual release, then runs this
suite on Linux and macOS in an OS/client-version matrix. Each job selects a
single profile, exercises its supported scopes, and uploads a JUnit report even
when tests fail. Run all profiles locally with:

```sh
npm ci
bash scripts/build-release.sh /absolute/scratch/release
MUXLIO_ACCEPTANCE_PREFIX=/absolute/scratch/release npm run test:acceptance
```

Set `MUXLIO_ACCEPTANCE_PROFILE=codex-0.167.0` to run one matrix cell locally.
Profile IDs are in `client-profiles.mjs`; unknown profiles fail instead of
silently running zero tests. The suite compares profiles with the installed
product's advertised integrations so a new supported client requires coverage.

Each test copies the installed product into an isolated prefix with spaces,
creates a fresh HOME, workspace and runtime, and runs `muxlio init --client`.
The fixture MCP client discovers the installed registration and launches its
command/args/environment. It checks tool discovery, skill/rule activation metadata,
workspace denial, two executor selections (`command` feeding `agent/a2a`), duplicate
and conflicting submissions, client exit, drained-worker queue recovery after
service restart, in-flight remote-task recovery without resubmission, run
rediscovery through MCP, eventual output, exactly two execution attempts, reinstall,
preserved results, adapter failure and operator policy denial. The A2A endpoint
is a protocol fixture; both adapters are real installed binaries. No coordinator,
worker or UI unit test needs client or provider branching.
The Codex project cell additionally restarts while the A2A task is detached,
then verifies that recovery polls its durable task ID without another submission.
Read-only poll attempts are counted separately from the two execution attempts.

The fixture profile versions are reported in every test name:

| Client | Fixture version | Scopes |
| --- | --- | --- |
| Claude Code | 2.1.319 | project, user |
| Codex | 0.167.0 | project, user |
| Cursor | 2.4.0 | project, user (Agent Skills) |
| Cursor | 2.3.0 | project (manual project rule) |
| Kiro | 2.37.0 | project, user |
| Antigravity | unavailable | registration must fail without writes |

These are pinned configuration/protocol profiles, **not execution of those
client versions**. Cursor's version discovery uses a fixture executable.
Antigravity's execution adapter has conformance coverage, but its authoring MCP
registration is explicitly unsupported in setup integrations. This suite retains
that boundary; it does not guess an undocumented configuration. Full five-client
acceptance remains blocked until a verified Antigravity registration is added.

Default upgrade coverage reinstalls the same release over the installed prefix
and reruns setup. To test a version-to-version upgrade, also set
`MUXLIO_ACCEPTANCE_UPGRADE_FROM=/absolute/older/installed/prefix`. Both prefixes
must support the tested setup/shared contract. Upgrade checks allow instruction
contents to change while checking that skill/rule activation remains valid.
Existing installer tests (`bash scripts/verify-install.sh PREFIX` and the
core CLI installer tests) cover
resource installation and migration; this suite verifies client reconnection and
persisted runs across the replacement.

## Credentialed real-client gate

`npm run test:acceptance:real` is intentionally opt-in and fails unless it runs
on macOS with at least two **different** configured clients. It never installs
an unpinned `latest` client. On an authenticated machine, set
`MUXLIO_ACCEPTANCE_PREFIX` and `MUXLIO_REAL_CLIENTS` to a JSON array:

```json
[
  {
    "client": "claude",
    "binary": "/absolute/path/to/claude",
    "versionArgv": ["--version"],
    "version": "EXACT COMPLETE TRIMMED --version OUTPUT",
    "argv": ["-p", "{prompt}", "--output-format", "stream-json", "--verbose"]
  },
  {
    "client": "codex",
    "binary": "/absolute/path/to/codex",
    "versionArgv": ["--version"],
    "version": "EXACT COMPLETE TRIMMED --version OUTPUT",
    "argv": ["exec", "--json", "--ephemeral", "--skip-git-repo-check", "--sandbox", "read-only", "{prompt}"]
  }
]
```

The placeholder must occupy an entire argument. Client flags belong in this
operator-owned setup manifest, not in workers or coordinator code. Cursor
(`cursor agent --print --output-format stream-json {prompt}`) and Kiro
(`kiro-cli chat --no-interactive --output-format stream-json {prompt}`) can be
selected with the same schema. Verify these flags against the exact installed
client version. Supply the ordinary client login and workspace/MCP approvals;
no approval-bypass flags are added by the harness. Fresh project trust may need
to be prepared on the dedicated runner. Real probes retain the normal HOME so
that existing client authentication remains available; they install Muxlio only
in temporary project scope.

Each client submits the run through MCP, exits, and starts a new session to
rediscover and retrieve the result after the installed service restarts. A
transparent MCP relay independently records successful tool names and comparison/terminal-success booleans only; model
prose cannot satisfy the gate. The coordinator must contain exactly one queued
run and eventually the expected command output. Both submissions must match the
requested arguments, and the terminal read must target that run. The relay never
records the compared arguments or run IDs. CLI versions, OS, architecture,
Node version, OS release, installed Muxlio version and per-client pass/fail are
reported by the Node test runner. The manual workflow uploads JUnit results;
it does not upload model transcripts or MCP arguments/results.
Fixture tests cover the two-adapter workflow; real probes use a deterministic
command executor so authoring-client credentials can be tested independently of
provider-executor credentials. They can consume model quota.
The prompts request skill activation, but the MCP evidence does not attest that
the client loaded the installed skill/rule. Fixture checks verify its metadata;
independent real-client skill activation evidence is still needed.

The manual `Credentialed client acceptance` workflow uses a dedicated
`[self-hosted, macOS, muxlio-acceptance]` runner and the `client-acceptance`
environment. Store the manifest in its `MUXLIO_REAL_CLIENTS` secret. Require
review of that workflow/ref in the environment configuration. It does not run
on push or pull-request events. Successful CI output from two clients is the
required real-client acceptance evidence; adding the harness is not evidence
that those clients have passed.

Harness regression tests run in normal unit CI, including manifest validation,
missing executables, early MCP exit, concurrent response correlation, and relay
rejection of failed calls, RPC errors and notification evidence.

Failures identify `client-configuration` (launch, MCP discovery, login, version,
trust/approval), `submission`/`workspace authorization`, `worker-adapter`
(execution or eventual result), or `upgrade` stages. Client transcripts and MCP
payloads are withheld from the credentialed probe's logs. No credentials are
needed or read by the fixture suite.
