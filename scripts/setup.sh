#!/usr/bin/env bash
set -euo pipefail
repo_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
runtime_dir="${MUXLIO_HOME:-${HOME}/.muxlio}"
mkdir -p "$runtime_dir"/{data,logs,work,artifacts,bin}
umask 077
chmod 700 "$runtime_dir"
mkdir -p "$repo_dir/.bin"
chmod 755 "$repo_dir/.bin"
bash "$repo_dir/scripts/build-release.sh" "$runtime_dir"
# Initialize protected runtime configuration and create the coordinator token
# during installation. Repeated setup runs preserve the existing token.
if [ -n "${MUXLIO_WORKSPACE:-}" ]; then
  "$runtime_dir/bin/muxlio" init --data-dir "$runtime_dir" --workspace "$MUXLIO_WORKSPACE" >/dev/null
elif [ -f "$runtime_dir/config.json" ]; then
  "$runtime_dir/bin/muxlio" init --data-dir "$runtime_dir" >/dev/null
else
  "$runtime_dir/bin/muxlio" init --data-dir "$runtime_dir" --workspace "$repo_dir" >/dev/null
fi
# Retain the development launcher's binary while the installed CLI and its
# resources live independently of this checkout.
cp "$runtime_dir/bin/muxlio" "$repo_dir/.bin/muxlio"
printf 'Setup complete. CLI: %s/bin/muxlio\nRuntime data: %s\n' "$runtime_dir" "$runtime_dir"
