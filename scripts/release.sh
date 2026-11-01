#!/usr/bin/env bash
set -euo pipefail
repo_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
runtime_dir="${MUXLIO_HOME:-${HOME}/.muxlio}"
bash "$repo_dir/scripts/build-release.sh" "$runtime_dir"
