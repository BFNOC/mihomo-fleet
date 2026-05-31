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
