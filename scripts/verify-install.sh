#!/usr/bin/env bash
# Verify a built prefix from a temporary copy, with no checkout at runtime.
# Usage: scripts/verify-install.sh /absolute/path/to/installed/prefix
set -euo pipefail

source="${1:?Usage: scripts/verify-install.sh /absolute/path/to/installed/prefix}"
source="$(cd -- "$source" && pwd)"

tmp="$(mktemp -d -t muxlio-install-test-XXXXXX)"
trap 'rm -rf "$tmp"' EXIT

prefix="$tmp/installed"
runtime="$tmp/runtime"
workspace="$tmp/workspace"
mkdir -p "$workspace"

cp -r "$source/." "$prefix"

# Write fake-echo adapter
adapter="$prefix/libexec/muxlio/adapters/fake-echo"
cat > "$adapter" <<'EOF'
#!/bin/sh
cat > request.json
sleep 0.2
printf '%s' '{"outputs":{"result":"installed result"}}'
EOF
chmod 700 "$adapter"

# Pick a free port
port="$(node -e 'const s = require("node:net").createServer().listen(0, "127.0.0.1", () => { console.log(s.address().port); s.close(); });')"
base="http://127.0.0.1:${port}"
binary="$prefix/bin/muxlio"
config_path="$tmp/config.json"
log="$tmp/service.log"

json_field() {
  local key="$1"
  node -e "let d='';process.stdin.on('data',c=>d+=c);process.stdin.on('end',()=>{const obj=JSON.parse(d);const v=obj['$key'];console.log(typeof v==='object'?JSON.stringify(v):v);})"
}

write_config() {
  local caps="$1"
  printf '{"runtimeDir":"%s","workspace":"%s","addr":"127.0.0.1:%s","capabilities":[%s]}' \
    "$runtime" "$workspace" "$port" "$caps" > "$config_path"
}

# Strip any ambient MUXLIO_ env vars so this test uses its own isolated state.
while IFS='=' read -r key _; do
  case "$key" in MUXLIO_*) unset "$key" ;; esac
done < <(env)
export MUXLIO_CONFIG="$config_path"

start_service() {
  write_config "$1"
  > "$log"
  "$binary" service start --config "$config_path" >> "$log" 2>&1
  # Wait up to 5 s for /healthz
  local i=0
  while [ "$i" -lt 200 ]; do
    if curl -sf "$base/healthz" -o /dev/null 2>/dev/null; then
      return
    fi
    sleep 0.025
    i=$((i + 1))
  done
  echo "service did not become healthy" >&2
  cat "$log" >&2
  exit 1
}

stop_service() {
  "$binary" service stop --config "$config_path" >> "$log" 2>&1
  local i=0
  while [ -f "$runtime/service.pid" ]; do
    sleep 0.1
    i=$((i + 1))
    if [ "$i" -gt 150 ]; then
      echo "service did not stop in time" >&2
      exit 1
    fi
  done
  if [ -f "$runtime/service.pid" ]; then
    echo "service.pid still exists after stop" >&2
    exit 1
  fi
}

cli() {
  "$binary" "$@" --server "$base"
}

# ── Phase 1: start with a capability the plan doesn't need ──────────────────
start_service '"fake/other"'

# UI present
body="$(curl -sf "$base/" 2>/dev/null)"
if ! printf '%s' "$body" | grep -q "Durable agent workflows"; then
  echo "installed UI missing" >&2; exit 1
fi

# Runs API accessible unauthenticated
status="$(curl -o /dev/null -sw '%{http_code}' "$base/v1/runs")"
if [ "$status" != "200" ]; then
  echo "runs API returned $status, expected 200" >&2; exit 1
fi

# Write plan + input
plan_path="$tmp/plan.json"
input_path="$tmp/input.json"
cat > "$plan_path" <<'EOF'
{
  "irVersion": "1.0",
  "name": "queued installed task",
  "root": "root",
  "subgraphs": {
    "root": {
      "nodes": [
        {"id": "work", "kind": "execute", "executor": "fake/echo", "outputs": {"result": {}}}
      ]
    }
  }
}
EOF
printf '{}' > "$input_path"

run_id="$(cli submit "$plan_path" "$input_path" | json_field runId)"

# Submission client has exited. No capable worker yet — run should still be running.
sleep 0.25
state="$(cli status "$run_id" | json_field state)"
if [ "$state" != "running" ]; then
  echo "expected state=running after submit with no capable worker, got: $state" >&2; exit 1
fi

stop_service

# ── Phase 2: restart with matching capability ────────────────────────────────
start_service '"fake/echo"'

i=0
while [ "$i" -lt 150 ]; do
  result="$(cli status "$run_id")"
  state="$(printf '%s' "$result" | json_field state)"
  if [ "$state" = "succeeded" ]; then break; fi
  if [ "$state" = "failed" ]; then
    echo "run failed: $result" >&2; exit 1
  fi
  sleep 0.1
  i=$((i + 1))
done
if [ "$state" != "succeeded" ]; then
  echo "run did not succeed: $result" >&2; exit 1
fi

output="$(printf '%s' "$result" | json_field output)"
if [ "$output" != "installed result" ]; then
  echo "unexpected output: $output" >&2; exit 1
fi

# Exactly one execution request on disk
requests="$(find "$runtime/work" -name "request.json" -path "*/exec-*" 2>/dev/null | wc -l | tr -d ' ')"
if [ "$requests" != "1" ]; then
  echo "expected 1 execution request, found $requests" >&2; exit 1
fi
req_file="$(find "$runtime/work" -name "request.json" -path "*/exec-*" | head -1)"
req_workspace="$(node -e "console.log(JSON.parse(require('fs').readFileSync(process.argv[1],'utf8')).workspace)" "$req_file")"
if [ "$req_workspace" != "$workspace" ]; then
  echo "request workspace mismatch: $req_workspace" >&2; exit 1
fi

stop_service

# ── Phase 3: result persists across a second restart ────────────────────────
start_service '"fake/echo"'

output2="$(cli status "$run_id" | json_field output)"
if [ "$output2" != "installed result" ]; then
  echo "persisted output mismatch after restart: $output2" >&2; exit 1
fi

stop_service

echo "Installed-prefix lifecycle, unauthenticated submit/status, queued restart recovery, explicit workspace and persisted result passed"
