#!/usr/bin/env bash
# Run `arcana serve` with runtime.driver: containerd and allowlist networking against a real containerd, and use it
# through the CLI and the REST API.
#
#   make build proxy-image templates && scripts/containerd-allowlist-e2e.sh
#
# Allowlist networking needs root (network namespaces, CNI plugins), so Arcana runs as root inside the throwaway
# containerd container (scripts/lib-containerd.sh), which also holds the CNI plugins and the proxy image. It checks
# doctor, an allowlisted request through the proxy, a refused one, no direct route, the audit trail, a restart that
# replaces the proxy, and that destroying leaves no namespaces or bridges behind. It reaches the real internet.
set -euo pipefail
EXTRA_IMAGES="arcana:local arcana-python:local"
source "$(dirname "${BASH_SOURCE[0]}")/lib-containerd.sh"

oci=runc
command -v runsc >/dev/null && oci=runsc
insecure=false
[ "$oci" = runc ] && insecure=true
data="$dir/data"
state=/run/arcana-e2e
port=18080
mkdir -p "$data"
cat >"$dir/arcana.yaml" <<YAML
server: {listen: 127.0.0.1:$port, metrics_listen: 127.0.0.1:19090}
data_dir: $data
runtime:
  driver: containerd
  containerd: {address: $dir/containerd.sock, fifo_dir: $dir/fifo, oci_runtime: $oci, allow_insecure_runtime: $insecure, allowlist: true, cni_bin_dir: /opt/cni/bin, state_dir: $state}
workspace: {backend: tmpfs}
network: {proxy_image: docker.io/library/arcana:local, proxy_dns: [1.1.1.1, 1.0.0.1]}
templates:
  - {name: py, image: arcana-python:local}
log: {level: info, format: json}
YAML
[ -x "$root/bin/arcana" ] || fail "missing bin/arcana (make build)"
# Arcana runs as root, so its files do too: read the key through the container, and remove its files from there.
admin_key() { docker exec "$name" cat "$data/admin.key" 2>/dev/null || true; }
# Everything Arcana does runs as root inside the containerd container, with the repository and $dir at the same paths.
inside() { docker exec -i -w "$root" -e ARCANA_URL="http://127.0.0.1:$port" -e ARCANA_API_KEY="$(admin_key)" "$name" "$@"; }
arcana() { inside "$root/bin/arcana" "$@"; }
stop() { docker exec "$name" sh -c 'kill -TERM "$(cat '"$dir"'/serve.pid)" 2>/dev/null; for i in $(seq 1 30); do kill -0 "$(cat '"$dir"'/serve.pid)" 2>/dev/null || exit 0; sleep 1; done' >/dev/null 2>&1 || true; }
finish() {
  status=$?
  [ "$status" -eq 0 ] || docker exec "$name" sh -c "tail -25 $dir/serve.log" 2>/dev/null | cut -c1-500 >&2 || true
  stop; docker exec "$name" rm -rf "$data" "$state" >/dev/null 2>&1 || true; cleanup
}
trap finish EXIT
start() {
  docker exec -d "$name" sh -c "echo \$\$ > $dir/serve.pid; exec $root/bin/arcana serve --config $dir/arcana.yaml >$dir/serve.log 2>&1"
  for _ in $(seq 1 60); do docker exec "$name" curl -fs "http://127.0.0.1:$port/v1/health" >/dev/null 2>&1 && return 0; sleep 1; done
  docker exec "$name" tail -20 "$dir/serve.log" >&2 || true
  fail "arcana serve did not become healthy"
}
py() { arcana exec "$1" -- python3 -c "$2"; }
fetch() { printf 'import urllib.request,urllib.error\ntry:\n print(urllib.request.urlopen("%s",timeout=20).status)\nexcept urllib.error.HTTPError as e:\n print("HTTP",e.code)\n' "$1"; }

echo "== doctor (runtime: $oci)"
docker exec -w "$root" "$name" "$root/bin/arcana" doctor --config "$dir/arcana.yaml" --json >"$dir/doctor.json" || { cat "$dir/doctor.json" >&2; fail "doctor reported a blocking failure"; }
python3 - "$dir/doctor.json" <<'PY'
import json, sys
checks = {c["name"]: c for c in json.load(open(sys.argv[1]))["checks"]}
assert checks["driver"]["status"] == "pass" and "allowlist networking" in checks["driver"]["detail"], checks["driver"]
assert checks["runtime"]["status"] == "pass", checks["runtime"]
print("driver:", checks["driver"]["detail"])
PY

echo "== serve"
start
docker exec "$name" grep -q '"driver":"containerd/' "$dir/serve.log" || fail "serve did not report the containerd driver"

echo "== an allowlist sandbox"
id="$(arcana sandbox create --template py --network allowlist --allow example.com --json | python3 -c 'import sys,json; print(json.load(sys.stdin)["id"])')"
[ "$(py "$id" "$(fetch http://example.com)")" = 200 ] || fail "an allowlisted host was not reachable through the proxy"
[ "$(py "$id" "$(fetch https://example.com)")" = 200 ] || fail "an allowlisted host was not reachable through CONNECT"
[ "$(py "$id" "$(fetch http://example.org)")" = "HTTP 403" ] || fail "a host off the allowlist was not refused"
direct="$(py "$id" 'import socket
try:
    socket.create_connection(("1.1.1.1",443),timeout=5); print("CONNECTED")
except OSError as e:
    print("blocked",e.errno)')"
[ "$direct" = "blocked 101" ] || fail "a direct connection must fail with no route, got: $direct"

echo "== the audit trail"
for _ in $(seq 1 15); do
  events="$(docker exec "$name" curl -fs -H "Authorization: Bearer $(admin_key)" "http://127.0.0.1:$port/v1/audit?action=egress.decision&sandbox_id=$id&limit=100")"
  python3 - "$events" <<'PY' && break || sleep 1
import json, sys
items = json.loads(sys.argv[1])["items"]
seen = {(e["details"]["host"], e["details"]["allowed"]) for e in items}
sys.exit(0 if ("example.com", True) in seen and ("example.org", False) in seen else 1)
PY
done
python3 - "$events" <<'PY' || fail "egress decisions missing from the audit trail: $events"
import json, sys
seen = {(e["details"]["host"], e["details"]["allowed"]) for e in json.loads(sys.argv[1])["items"]}
assert ("example.com", True) in seen and ("example.org", False) in seen, seen
PY

echo "== a restart replaces the proxy and keeps the policy"
stop
start
[ "$(py "$id" "$(fetch http://example.com)")" = 200 ] || { docker exec "$name" tail -15 "$dir/serve.log" >&2 || true; fail "the allowlisted host stopped working after the restart"; }
[ "$(py "$id" "$(fetch http://example.org)")" = "HTTP 403" ] || fail "the allowlist stopped applying after the restart"

echo "== destroying removes the network"
arcana sandbox rm "$id" >/dev/null
left="$(docker exec "$name" sh -c "ls $state/netns 2>/dev/null; ip -o link show | grep -E ' arc[a-z2-7]{12}[:@]' || true")"
[ -z "$left" ] || fail "network leftovers after destroy: $left"
echo "containerd allowlist e2e passed"
