# Run dependent features in a Docker sandbox

Run a batch in a private Git clone inside a named [Docker sandbox](https://docs.docker.com/ai/sandboxes/). The host runs the Muxlio coordinator and dashboard. One worker inside the sandbox offers Claude Code, Codex, Cursor, and the `command` executor for builds, tests, and Git. Each feature starts from the preceding feature's verified local commit, so separate agent sessions can build on the same code.

This example uses Muxlio's own repository as the project and provisions the worker manually. Docker Sandboxes manages the environment and Muxlio manages the run.

## Choose a worker by workspace

Choose the **workspace** to select the worker and checkout. Both workers can advertise `agent/codex`, and the sandbox worker also offers Claude Code, Cursor, and `command`.

| Workspace ID | Owning worker | Executors in this example | Checkout |
| --- | --- | --- | --- |
| `localhost` | `local` | `agent/codex`, `command` | Host checkout |
| `sandbox` | `sbx` | `agent/claude`, `agent/codex`, `agent/cursor`, `command` | Private sandbox clone |

Leave `workspaceId` unset in the workflow definition so you can reuse the same version in either place. In the dashboard, choose **Run workflow**, select the workspace, and run **Preflight**. On the CLI, pass `--workspace-id localhost` or `--workspace-id sandbox` to both `muxlio preflight` and `muxlio run`. The coordinator routes workspace steps to the selected owner. Preflight reports that owner and whether it can run the requested executor. [Select an authorized project per run](service.md#select-an-authorized-project-per-run) · [Dashboard launch](ui-operations.md#launch-a-workflow)

## Create the sandbox

On the host, [install Muxlio](service.md#install) and a version of [`sbx`](https://docs.docker.com/ai/sandboxes/install/) that supports v3 kits. Sign in to Docker and provision the three provider credentials. These commands prompt for API keys and let Docker manage their injection. For Codex, you can use `sbx secret set openai --oauth` for a ChatGPT login. [Claude Code mixin](https://hub.docker.com/r/sbx/claude-mixin) · [Codex mixin](https://hub.docker.com/r/sbx/codex-mixin) · [Cursor mixin](https://hub.docker.com/r/sbx/cursor-mixin)

```sh
sbx login
sbx secret set anthropic
sbx secret set openai
sbx secret set cursor
```

From the main checkout of the Muxlio repository, create a sandbox named `sbx`, matching the worker ID. It starts at the current Git revision. Use Docker's shell workload as the base and add each agent as a mixin. [Combine multiple mixins](https://docs.docker.com/ai/sandboxes/customize/use-kits/#combine-multiple-mixins)

```sh
muxlio init --workspace "$PWD"
sbx create --clone --name sbx sbx/shell:latest "$PWD" \
  --kit sbx/claude-mixin:latest \
  --kit sbx/codex-mixin:latest \
  --kit sbx/cursor-mixin:latest
sbx policy allow network localhost:7600
```

Approve the credential bindings when Docker prompts. Clone mode gives the worker its own Git repository and lets you fetch its commits back locally. The network rule lets the worker reach the host's loopback coordinator as `host.docker.internal:7600`. [Docker Git workflow](https://docs.docker.com/ai/sandboxes/workflows/git/#clone-mode) · [Accessing host services](https://docs.docker.com/ai/sandboxes/workflows/development/#accessing-host-services-from-a-sandbox)

## Prepare the sandbox worker

Open a shell on the sandbox checkout.

```sh
sbx exec -it sbx bash
```

Inside the sandbox, make Go (matching `muxlio-core/go.mod`), Node.js 22.13 or later, and npm available on `PATH`. Install the project's dependencies and build the Linux worker and all four adapters from the clone. Use the same Muxlio revision for the host coordinator and the worker.

```sh
npm ci
git switch -c muxlio/feature-batch
git config --local user.name "Your Name"
git config --local user.email "you@example.com"

runtime="$HOME/.muxlio"
mkdir -p "$runtime/bin" "$runtime/adapters"
(cd muxlio-core && go build -o "$runtime/bin/muxlio" ./cmd/muxlio)
(
  cd muxlio-adapters
  for adapter in agent-claude agent-codex agent-cursor command; do
    go build -o "$runtime/adapters/$adapter" "./cmd/$adapter" || exit
    cp "cmd/$adapter/adapter.json" "$runtime/adapters/$adapter.json" || exit
  done
)

"$runtime/bin/muxlio" init --data-dir "$runtime" --workspace "$PWD"
"$runtime/bin/muxlio" workspace add sandbox "$(git rev-parse --show-toplevel)" \
  --data-dir "$runtime" --worker-id sbx
```

Merge these `env` entries into the existing executor settings in the sandbox's `$HOME/.muxlio/config.json`. Muxlio filters the worker's ambient environment, so the adapter receives its placeholder through its own configuration. Docker's proxy supplies the real keys from the host. If you used Codex OAuth, leave out the OpenAI API key placeholder. The Cursor setting points the adapter at the executable supplied by Docker's mixin. [Docker credential placeholders](https://docs.docker.com/ai/sandboxes/configuration/credentials/#custom-templates-and-placeholder-values)

```json
{
  "executors": {
    "agent/claude": {
      "env": { "ANTHROPIC_API_KEY": "proxy-managed" }
    },
    "agent/codex": {
      "env": { "OPENAI_API_KEY": "proxy-managed" }
    },
    "agent/cursor": {
      "env": {
        "CURSOR_AGENT": "/usr/local/bin/cursor-agent",
        "CURSOR_API_KEY": "proxy-managed"
      }
    }
  }
}
```

Check the worker before you submit the workflow.

```sh
"$runtime/bin/muxlio" worker status --data-dir "$runtime" --adapters "$runtime/adapters"
```

Check that all four executors report `ready=true`. If an agent reports an authentication error, check its Docker credential binding and the CLI's login status from this shell. Muxlio uses the worker's configured environment and runs the agent CLI headlessly. Use [worker environment overrides](architecture/service-reference.md#shared-worker-environment) if a CLI needs additional configuration. Keep the workspace binding free of a content digest because the recipe changes the checkout between features. Exit the shell when setup is complete.

## Register and start the worker

On the host, register the same workspace ID and owner using the path **inside the sandbox**. Start the service, or restart it if it is already running.

```sh
sandbox_checkout=$(sbx exec sbx git rev-parse --show-toplevel)
muxlio workspace add sandbox "$sandbox_checkout" --worker-id sbx
muxlio service start
```

To also offer a host checkout in the workspace picker, run the following on the host and restart the service. Use a checkout you want Muxlio to edit and authenticate the host's Codex CLI if you want to select Codex there. With explicit workspace bindings configured, register each checkout you want to offer.

```sh
muxlio workspace add localhost /absolute/path/to/local/checkout --worker-id local
muxlio service restart
```

The recipe produces new artifacts during execution, including implementation notes and review reports. Its worker needs the coordinator's cluster `workerToken` for those uploads. Digest-scoped artifact grants cover predeclared digests. Use this setup with a coordinator and workers you trust, since the cluster token permits work and artifact transfer across that coordinator. Keep the administrator token on the host. [Worker credentials](architecture/worker-protocol.md#versioning)

On the host, open `~/.muxlio/config.json` and copy the value of `workerToken`. If the coordinator uses a custom config path or `MUXLIO_WORKER_TOKEN`, copy its active worker token instead. Paste it into the hidden prompt below. Bash sends it to a private file in the sandbox.

```sh
bash <<'BASH'
IFS= read -r -s -p 'Muxlio worker token: ' worker_token </dev/tty || exit 1
printf '\n' >&2
if [[ -z "$worker_token" ]]; then
  printf 'A worker token is required.\n' >&2
  exit 1
fi
printf '%s\n' "$worker_token" |
  sbx exec -i sbx sh -c 'umask 077; cat > "$HOME/.muxlio/host-worker-token"'
BASH
```

Start the worker from a host terminal and keep it running for the batch. The `--capabilities` list makes all four executors available on this workspace.

```sh
sbx exec sbx sh -c '
  exec "$HOME/.muxlio/bin/muxlio" worker start \
    --coordinator http://host.docker.internal:7600 \
    --worker-id sbx \
    --data-dir "$HOME/.muxlio" \
    --adapters "$HOME/.muxlio/adapters" \
    --worker-token-file "$HOME/.muxlio/host-worker-token" \
    --capabilities agent/claude,agent/codex,agent/cursor,command
'
```

## Run two features on the same branch

In another host terminal, open the Muxlio source checkout. The [example input](https://github.com/Muxlio/muxlio/blob/main/recipes/implement-features/input.example.json) queues these two features in order.

1. Add and test a plain-text run summary formatter for the CLI.
2. Add a `status --summary` option that uses the formatter from the first feature.

Check that the coordinator sees the workspace and that preflight reports a feasible run. Use `local`, the coordinator profile created by `muxlio init`.

```sh
npm ci
npm run build --workspace @muxlio/sdk
muxlio workspace list --server local
muxlio preflight recipes/implement-features/workflow.ts recipes/implement-features/input.example.json \
  --server local --workspace-id sandbox
muxlio run recipes/implement-features/workflow.ts recipes/implement-features/input.example.json \
  --server local --workspace-id sandbox
```

Muxlio implements and reviews the first feature, runs the repository's build and tests inside the sandbox, and commits after the checks pass. The second feature then starts with the formatter already committed. Failed checks feed a bounded repair loop. After ten unsuccessful verification attempts, the run pauses on that feature. Follow logs, notes, and test results in the host dashboard at `http://localhost:7600`.

The recipe uses `agent/any` for implementation, review, and repair, so Muxlio selects an eligible ready agent for each step. To assign specific agents, edit the corresponding tasks in `tasks.ts`, for example `agent/codex` for implementation, `agent/claude` for review, and `agent/cursor` for repair. The repair step runs when verification fails. The `command` executor handles verification and commits in the same clone.

To queue more features during the run, use `recipes/implement-features/live.ts` in both commands. Open the run and choose **Add item** in the **features** collection. Items run in acceptance order. [Manage live collections](ui-operations.md#add-items-to-a-running-workflow)

For a different project, adapt the recipe's [verification commands](https://github.com/Muxlio/muxlio/blob/main/recipes/implement-features/tasks.ts) to that project's build and tests. Register the project's sandbox checkout and submit the adapted recipe against that workspace ID.

## Fetch and review the commits

From the same host checkout used to create the sandbox, fetch the batch branch for review.

```sh
git fetch sandbox-sbx refs/heads/muxlio/feature-batch
git log --oneline -2 FETCH_HEAD
```

Once you want to keep the result, save the fetched tip to a local branch, for example `git branch review/muxlio-feature-batch FETCH_HEAD`. Export any commits you want to retain before removing the sandbox. [Docker Git workflow](https://docs.docker.com/ai/sandboxes/workflows/git/#sandbox-remote-behavior)
