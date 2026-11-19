| Port | Role after `muxlio init` + `muxlio service start` |
| --- | --- |
| **7600** | Coordinator HTTP API and bundled observability UI (`addr`) |
| **7602** | Streamable HTTP MCP when `mcp` is set in config (`mcp.listen`, default from `init`) |
| **7601** | Optional `muxlio ui` or `npm --prefix muxlio-ui start` only (proxies to the coordinator) |

`muxlio init --client` registers stdio `muxlio-mcp` for **`claude`**, **`codex`**, **`cursor`**, and **`kiro`**. **`antigravity`** installs authoring instructions only; MCP registration via `init` or `integrations add` is rejected until that client's CLI contract is verified. Use `integrations add` when `init` omitted `--client` or to repair registration.