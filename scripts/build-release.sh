#!/usr/bin/env bash
set -euo pipefail
repo="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
destination="${1:?Usage: scripts/build-release.sh /absolute/output/prefix}"
case "$destination" in /*) ;; *) echo 'Output prefix must be absolute' >&2; exit 2;; esac
version_file="$repo/VERSION"
if [ -f "$version_file" ]; then
  version="$(tr -d '[:space:]' < "$version_file")"
else
  version="${MUXLIO_VERSION:-0.2.0}"
fi

mkdir -p "$destination/bin" "$destination/libexec/muxlio/adapters" "$destination/share/muxlio/ui" "$destination/share/muxlio/skills" "$destination/skills"
build_tmp="$destination/bin/.muxlio-build"
(cd "$repo/muxlio-core" && go build -trimpath -ldflags "-X main.CurrentVersion=$version" -o "$build_tmp" ./cmd/muxlio)
mv "$build_tmp" "$destination/bin/muxlio"
(cd "$repo/muxlio-core" && go build -trimpath -ldflags "-X main.Version=$version" -o "$destination/bin/muxlio-mcp" ./cmd/muxlio-mcp)
for adapter in agent-a2a agent-acp agent-antigravity agent-claude agent-codex agent-cursor agent-kiro command; do
  (cd "$repo/muxlio-adapters" && go build -trimpath -o "$destination/libexec/muxlio/adapters/$adapter" "./cmd/$adapter")
  if [ -f "$repo/muxlio-adapters/cmd/$adapter/adapter.json" ]; then
    cp "$repo/muxlio-adapters/cmd/$adapter/adapter.json" "$destination/libexec/muxlio/adapters/$adapter.json"
  fi
done
# Retire the old bundled adapter when upgrading an existing runtime prefix.
rm -f -- "$destination/libexec/muxlio/adapters/command-go-test"
(cd "$repo" && npm run build --workspace muxlio-ui)
cp "$repo/muxlio-ui/index.html" "$repo/muxlio-ui/logo.png" "$repo/muxlio-ui/favicon.ico" \
  "$destination/share/muxlio/ui/"
mkdir -p "$destination/share/muxlio/ui/dist"
cp "$repo/muxlio-ui/dist/server.mjs" "$repo/muxlio-ui/dist/app.js" "$repo/muxlio-ui/dist/app.js.map" "$repo/muxlio-ui/dist/app.css" "$destination/share/muxlio/ui/dist/"
cp "$repo/LICENSE" "$destination/share/muxlio/LICENSE"
if [ -f "$repo/muxlio-skills/SKILL.md" ]; then
  cp "$repo/muxlio-skills/SKILL.md" "$destination/share/muxlio/skills/SKILL.md"
  cp "$repo/muxlio-skills/SKILL.md" "$destination/skills/SKILL.md"
fi

# Write environment file for shell sourcing
cat > "$destination/env" <<EOF
# Muxlio environment configuration
export MUXLIO_HOME="$destination"
case ":\$PATH:" in
  *":\$MUXLIO_HOME/bin:"*) ;;
  *) export PATH="\$MUXLIO_HOME/bin:\$PATH" ;;
esac
EOF
chmod 600 "$destination/env"

# Write version file
printf '%s\nupdated=%s\n' "$version" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$destination/version"

printf 'Self-contained runtime installed at %s\n' "$destination"
