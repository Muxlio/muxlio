# Documentation snippets

MkDocs expands `--8<-- "snippets/…"` via `pymdownx.snippets`. Edit these files
once when defaults change; every including page stays aligned.

| Snippet | Use |
| --- | --- |
| `root-context.md` | Top-level `muxlio-docs/*.md` (product + security pointer) |
| `reference-context.md` | `muxlio-docs/architecture/*.md` (product + security/isolation) |
| `errors-context.md` | `muxlio-docs/errors/*.md` |
| `setup-defaults.md` | Install and MCP setup facts (ports, `init --client`) |

Canonical entry points:

- [What Muxlio is](../index.md#what-is-muxlio) — vocabulary
- [Setup defaults](../service.md#setup-defaults) — ports **7600 / 7602 / 7601** and MCP `init --client` rules (enforced by `cmd/muxlio/docs_consistency_test.go`)

## Documentation areas (MkDocs nav)

Use this map when editing one topic so sibling pages stay linked.

| Area | Primary pages | Pair with |
| --- | --- | --- |
| **Home** | `index.md` | `service.md`, `architecture/overview.md` |
| **Choosing an adapter** | `adapters.md` | `developer-reference.md#run-with-real-adapters`, `accounting.md`, `execution-security.md` |
| **Service** | `service.md`, `snippets/setup-defaults.md` | `service-reference.md`, `worker-protocol.md`, `mcp.md` |
| **Docker Sandbox** | `docker-sandbox.md` | `execution-security.md#docker-sandboxes-sbx-and-muxlio-workers`, `service.md`, `ui-operations.md` |
| **MCP** | `mcp.md`, `architecture/mcp-reference.md` | `workflow-templates.md`, `execution-security.md` (grants, compilation) |
| **Workflow templates** | `workflow-templates.md` | `mcp.md`, `mcp-reference.md` (catalog tools) |
| **UI** | `ui-operations.md` | `operations-reference.md`, `execution-security.md#remote-web-and-mcp-access` |
| **Accounting** | `accounting.md`, `usd-cost-recovery.md` | `durable-execution.md` (run limits), `adapters.md` (reporting) |
| **Benchmarks** | `benchmarks.md` | `architecture/benchmark-methodology.md` |
| **Execution contracts** | `durable-execution.md`, `worker-protocol.md` | `execution-security.md`, `artifact-storage.md` |
| **Storage & handoff** | `artifact-storage.md`, `workspace-handoff.md`, `environment-manifests.md` | `execution-security.md`, `service.md#select-an-authorized-project-per-run` |
| **Security** | `execution-security.md` | `docker-sandbox.md`, `worker-protocol.md`, `mcp-reference.md` |
| **Developer / service reference** | `developer-reference.md`, `service-reference.md` | Above guides; guarded by `readme_test.go`, `executors_test.go` |
| **Operations API** | `operations-reference.md` | `ui-operations.md`, `execution-security.md` |
| **Errors** | `errors/errors.md` | Plan validation in `mcp-reference.md#check_plan`, `developer-reference.md` |

Tests that guard doc drift:

- `muxlio-core/cmd/muxlio/docs_consistency_test.go` — setup defaults, execution-security ↔ docker-sandbox
- `muxlio-core/cmd/muxlio/readme_test.go` — `developer-reference.md`
- `muxlio-core/cmd/muxlio/executors_test.go` — bundled executors in `service-reference.md`
- `muxlio-adapters/adapters_guide_test.go` — `adapters.md` vs shipped manifests