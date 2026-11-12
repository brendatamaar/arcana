#!/usr/bin/env bash
# Run a command against a throwaway live Arcana (used by the SDK live tests).
#
#   scripts/with-arcana.sh <command> [args...]
#
# Builds nothing: run `make build` first. The command gets ARCANA_URL, ARCANA_API_KEY and
# ARCANA_LIVE=1. The runtime is runsc when Docker has it registered, otherwise runc with the
# explicit insecure opt-in (CI and development hosts without gVisor). Override with
# ARCANA_OCI_RUNTIME. The sandbox template image must exist (`make templates`).
# ARCANA_LIVE_WARM_POOL sets the base template's warm pool (default 0). Any ARCANA_* variable in the
# caller's environment, such as ARCANA_LIMITS__MAX_SANDBOXES, overrides the generated config as usual.
set -euo pipefail

if [ "$#" -eq 0 ]; then
  echo "usage: $0 <command> [args...]" >&2
  exit 2
fi
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
binary="${ARCANA_BIN:-$root/bin/arcana}"
[ -x "$binary" ] || { echo "missing $binary: run make build" >&2; exit 1; }

runtime="${ARCANA_OCI_RUNTIME:-}"
if [ -z "$runtime" ]; then
  if docker info --format '{{json .Runtimes}}' 2>/dev/null | grep -q '"runsc"'; then runtime=runsc; else runtime=runc; fi
fi
insecure=false
[ "$runtime" = runc ] && insecure=true

work="$(mktemp -d "${TMPDIR:-/tmp}/arcana-live-XXXXXX")"
pid=""
cleanup() {
  if [ -n "$pid" ]; then
    kill -TERM "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  fi
  rm -rf "$work"
}
trap cleanup EXIT
trap 'exit 1' TERM INT

free_port() {
  python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])'
}
api_port="$(free_port)"
metrics_port="$(free_port)"
cat > "$work/config.yaml" <<YAML
server:
  listen: 127.0.0.1:${api_port}
  metrics_listen: 127.0.0.1:${metrics_port}
  destroy_on_shutdown: true
data_dir: ${work}/data
runtime:
  docker:
    oci_runtime: ${runtime}
    allow_insecure_runtime: ${insecure}
templates:
  - name: base
    image: arcana-base:local
    warm_pool: ${ARCANA_LIVE_WARM_POOL:-0}
YAML
umask 077
"$binary" serve --config "$work/config.yaml" > "$work/serve.log" 2>&1 &
pid=$!

url="http://127.0.0.1:${api_port}"
for _ in $(seq 1 120); do
  if curl -fsS "$url/v1/health" > /dev/null 2>&1; then break; fi
  if ! kill -0 "$pid" 2>/dev/null; then
    echo "arcana serve exited early:" >&2
    tail -20 "$work/serve.log" >&2
    exit 1
  fi
  sleep 0.5
done
curl -fsS "$url/v1/health" > /dev/null || { echo "arcana did not become healthy" >&2; tail -20 "$work/serve.log" >&2; exit 1; }

echo "live Arcana on $url (runtime $runtime)" >&2
ARCANA_URL="$url" ARCANA_API_KEY="$(cat "$work/data/admin.key")" ARCANA_LIVE=1 "$@"
