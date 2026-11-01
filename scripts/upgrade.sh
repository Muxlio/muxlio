#!/usr/bin/env bash
# Upgrade Muxlio runtime in ~/.muxlio while preserving all data, database, and configuration.
set -euo pipefail

repo_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
runtime_dir="${MUXLIO_HOME:-${HOME}/.muxlio}"

printf "Upgrading Muxlio in %s...\n" "$runtime_dir"
bash "$repo_dir/scripts/build-release.sh" "$runtime_dir"

if [ -d "$repo_dir/.bin" ]; then
  cp "$runtime_dir/bin/muxlio" "$repo_dir/.bin/muxlio"
fi

printf "\nUpgrade complete! Run 'muxlio doctor' to verify system health.\n"
"$runtime_dir/bin/muxlio" version 2>/dev/null || true

