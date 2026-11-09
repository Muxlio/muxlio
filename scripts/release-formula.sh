#!/usr/bin/env bash
set -euo pipefail

repo="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
formula="$repo/Formula/muxlio.rb"
version="${1:?Usage: scripts/release-formula.sh <version> (for example 0.2.1)}"
version="${version#v}"
if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]]; then
  echo "Invalid release version: $version" >&2
  exit 2
fi
tag="v$version"
workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

if ! git -C "$repo" diff --quiet || ! git -C "$repo" diff --cached --quiet; then
  echo "Commit or stash tracked changes before releasing" >&2
  exit 1
fi
if git -C "$repo" rev-parse -q --verify "refs/tags/$tag" >/dev/null; then
  echo "Tag already exists: $tag" >&2
  exit 1
fi

git -C "$repo" tag -a "$tag" -m "Muxlio $tag"
git -C "$repo" push origin "$tag"
archive="$workdir/muxlio-$tag.tar.gz"
curl --fail --location --silent --show-error \
  "https://github.com/Muxlio/muxlio/archive/refs/tags/$tag.tar.gz" \
  --output "$archive"
sha="$(shasum -a 256 "$archive" | awk '{print $1}')"

python3 - "$formula" "$version" "$tag" "$sha" <<'PY'
from pathlib import Path
import re
import sys

path = Path(sys.argv[1])
version, tag, sha = sys.argv[2:]
source = path.read_text()
if not re.search(r'^  url ', source, re.M):
    source = source.replace('  head ', f'  url "https://github.com/Muxlio/muxlio/archive/refs/tags/{tag}.tar.gz"\n  version "{version}"\n  sha256 "{sha}"\n  head ', 1)

source, url_count = re.subn(r'url "https://github\.com/Muxlio/muxlio/archive/refs/tags/[^" ]+\.tar\.gz"',
                            f'url "https://github.com/Muxlio/muxlio/archive/refs/tags/{tag}.tar.gz"', source, count=1)
source, version_count = re.subn(r'version "[^"]+"', f'version "{version}"', source, count=1)
source, sha_count = re.subn(r'sha256 "[^"]+"', f'sha256 "{sha}"', source, count=1)
if url_count != 1 or version_count != 1 or sha_count != 1:
    raise SystemExit("Could not uniquely update formula URL, version, and sha256")
path.write_text(source)
PY

printf 'Tagged %s, archive sha256 %s\nUpdated %s\n' "$tag" "$sha" "$formula"
printf 'Review and commit Formula/muxlio.rb, then push the formula repository update.\n'
