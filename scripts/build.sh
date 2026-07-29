#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="${1:-"$ROOT/mihomo-fleet"}"
VERSION="$(tr -d '[:space:]' < "$ROOT/VERSION")"
COMMIT="$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || printf 'unknown')"
BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

if ! git -C "$ROOT" diff --quiet --ignore-submodules HEAD -- 2>/dev/null; then
  COMMIT="${COMMIT}-dirty"
fi

# The WebUI is compiled into the binary by go:embed, and its build output is not
# in git -- only internal/app/web/README.md is, because `go:embed web/*` fails to
# compile against an empty directory. That README is also why a missing frontend
# build does NOT fail the Go build: it embeds fine and ships a binary that serves
# no UI at all. So the frontend is built here, before Go, and the result is
# checked. Set SKIP_WEB=1 to reuse the existing internal/app/web/ (the check
# below still runs).
WEB_DIR="$ROOT/internal/app/web"
if [ "${SKIP_WEB:-0}" = "1" ]; then
  printf 'Skipping frontend build (SKIP_WEB=1)\n'
else
  if [ ! -d "$ROOT/node_modules" ]; then
    printf 'node_modules is missing; run `pnpm install` first.\n' >&2
    exit 1
  fi
  # scripts/build-web.mjs is what `pnpm build:web` runs. Invoked directly because
  # `pnpm run` first does a deps-status check that can decide to purge and
  # reinstall node_modules, and with no TTY it aborts mid-removal.
  node "$ROOT/scripts/build-web.mjs"
fi

for asset in index.html app.js styles.css; do
  if [ ! -f "$WEB_DIR/$asset" ]; then
    printf 'Frontend asset %s is missing from %s; the binary would serve no UI.\n' "$asset" "$WEB_DIR" >&2
    printf 'Run `pnpm install && pnpm build:web`, or drop SKIP_WEB.\n' >&2
    exit 1
  fi
done

mkdir -p "$(dirname "$OUT")"

(
  cd "$ROOT"
  go build \
    -trimpath \
    -ldflags "-s -w -X main.version=$VERSION -X main.commit=$COMMIT -X main.buildDate=$BUILD_DATE" \
    -o "$OUT" \
    ./cmd/mihomo-fleet
)

printf 'Built %s\n' "$OUT"
printf 'Version: %s\n' "$VERSION"
printf 'Commit: %s\n' "$COMMIT"
printf 'Build date: %s\n' "$BUILD_DATE"
