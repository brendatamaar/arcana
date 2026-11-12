#!/usr/bin/env bash
# Re-record the terminal GIFs in docs/tapes against a throwaway Arcana.
#
#   scripts/record-tapes.sh [doctor|quickstart|fork ...]
#
# Needs: a built bin/arcana (make build-all), vhs, ttyd, ffmpeg and a Chrome or Chromium on PATH,
# jq, and Docker with the base and python template images (make templates). Each tape gets its own
# Arcana, using runsc when Docker has it registered (see scripts/with-arcana.sh).
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"
for tool in vhs ttyd ffmpeg jq; do
  command -v "$tool" > /dev/null || { echo "missing required tool: $tool" >&2; exit 1; }
done
[ -x bin/arcana ] || { echo "missing bin/arcana: run make build-all" >&2; exit 1; }

shims="$(mktemp -d)"
trap 'rm -rf "$shims"' EXIT
ln -s "$root/bin/arcana" "$shims/arcana"

tapes=("$@")
[ "${#tapes[@]}" -gt 0 ] || tapes=(doctor quickstart fork)
for tape in "${tapes[@]}"; do
  echo "recording $tape" >&2
  ARCANA_LIVE_TEMPLATES=python PATH="$shims:$PATH" scripts/with-arcana.sh vhs "docs/tapes/$tape.tape"
done
