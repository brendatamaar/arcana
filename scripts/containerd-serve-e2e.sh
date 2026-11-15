#!/usr/bin/env bash
# Run `arcana serve` with runtime.driver: containerd against a real containerd, and use it through the CLI.
#
#   make build templates && scripts/containerd-serve-e2e.sh
#
# Starts a throwaway containerd (scripts/lib-containerd.sh), a real Arcana with a generated config, and checks
# doctor, sandbox create, exec, files, snapshot, fork, an unsupported request, a restart and clean shutdown.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib-containerd.sh"

oci=runc
command -v runsc >/dev/null && oci=runsc
insecure=false
[ "$oci" = runc ] && insecure=true
data="$dir/data"
mkdir -p "$data"
port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')"
metrics="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')"
cat >"$dir/arcana.yaml" <<YAML
server: {listen: 127.0.0.1:$port, metrics_listen: 127.0.0.1:$metrics}
data_dir: $data
runtime:
  driver: containerd
  containerd: {address: $dir/containerd.sock, fifo_dir: $dir/fifo, oci_runtime: $oci, allow_insecure_runtime: $insecure}
workspace: {backend: tmpfs}
templates:
  - {name: base, image: arcana-base:local}
log: {level: info, format: json}
YAML
arcana="$root/bin/arcana"
[ -x "$arcana" ] || fail "missing bin/arcana (make build)"
pid=""
stop() { [ -z "$pid" ] || { kill -TERM "$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true; pid=""; }; }
trap 'stop; cleanup' EXIT
start() {
  "$arcana" serve --config "$dir/arcana.yaml" >"$dir/serve.log" 2>&1 &
  pid=$!
  for _ in $(seq 1 60); do curl -fs "http://127.0.0.1:$port/v1/health" >/dev/null 2>&1 && return 0; sleep 1; done
  tail -20 "$dir/serve.log" >&2
  fail "arcana serve did not become healthy"
}

echo "== doctor with the containerd driver (runtime: $oci)"
"$arcana" doctor --config "$dir/arcana.yaml" --json >"$dir/doctor.json" || { cat "$dir/doctor.json" >&2; fail "doctor reported a blocking failure"; }
python3 - "$dir/doctor.json" <<'PY'
import json, sys
checks = {c["name"]: c for c in json.load(open(sys.argv[1]))["checks"]}
for name in ("driver", "runtime"):
    assert checks[name]["status"] == "pass", checks[name]
for absent in ("docker", "subnets", "runsc"):
    assert absent not in checks, absent
print("driver:", checks["driver"]["detail"]); print("runtime:", checks["runtime"]["detail"])
PY

echo "== serve"
start
export ARCANA_URL="http://127.0.0.1:$port" ARCANA_API_KEY="$(cat "$data/admin.key")"
grep -q '"driver":"containerd/' "$dir/serve.log" || fail "serve did not report the containerd driver"

echo "== create, exec, files"
id="$("$arcana" sandbox create --template base --json | python3 -c 'import sys,json; print(json.load(sys.stdin)["id"])')"
[ "$("$arcana" exec "$id" -- sh -c 'id -u; echo hi > /workspace/f; cat /workspace/f' | tr '\n' ' ')" = "1000 hi " ] || fail "exec"
printf 'payload' | "$arcana" files put "$id" - /workspace/up
[ "$("$arcana" files get "$id" /workspace/up -)" = "payload" ] || fail "files round trip"
status=0
"$arcana" exec "$id" -- sh -c 'exit 7' 2>/dev/null || status=$?
[ "$status" -eq 7 ] || fail "the remote exit code was not returned (got $status)"

echo "== snapshot and fork"
snap="$("$arcana" snapshot create "$id" --json | python3 -c 'import sys,json; print(json.load(sys.stdin)["id"])')"
fork="$("$arcana" fork "$snap" --count 2 --json | python3 -c 'import sys,json; d=json.load(sys.stdin); print(d["items"][0]["id"])')"
[ "$("$arcana" exec "$fork" -- cat /workspace/f)" = "hi" ] || fail "a fork lost the workspace"

echo "== an allowlist request is refused cleanly"
code="$(curl -s -o "$dir/refused.json" -w '%{http_code}' -X POST "$ARCANA_URL/v1/sandboxes" -H "Authorization: Bearer $ARCANA_API_KEY" -H 'Content-Type: application/json' -d '{"template":"base","network":{"mode":"allowlist","allow":["example.com"]}}')"
[ "$code" = 400 ] && grep -q invalid_request "$dir/refused.json" || fail "allowlist was not refused with 400 invalid_request ($code)"

echo "== a restart keeps the sandboxes"
stop
start
after="$("$arcana" exec "$id" -- cat /workspace/f 2>&1)" || { grep -v '"level":"INFO"' "$dir/serve.log" | tail -8 >&2 || true; fail "exec after the restart: $after"; }
[ "$after" = "hi" ] || fail "the sandbox lost its workspace across the restart"
for sandbox in "$id" "$fork"; do "$arcana" sandbox rm "$sandbox" >/dev/null || { tail -12 "$dir/serve.log" >&2; ctr tasks ls >&2 || true; ctr containers ls >&2 || true; docker logs "$name" 2>&1 | tail -12 | cut -c1-300 >&2; fail "destroying $sandbox after the restart"; }; done
echo "containerd serve e2e passed"
