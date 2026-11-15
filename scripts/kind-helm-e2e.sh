#!/usr/bin/env bash
# Install the Helm chart on a throwaway kind cluster and check that it really works.
#
#   make build proxy-image templates && scripts/kind-helm-e2e.sh
#
# Needs docker, kind, kubectl and helm on PATH, and the arcana:local and arcana-base:local images.
# The kind node mounts the HOST's Docker socket, so the chart controls the host Docker Engine, and a
# data directory at the same path on the host, the node and the pod (the chart's requirement).
# Sandboxes are real containers on the host; everything is removed on exit.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cluster="arcana-e2e"
namespace="arcana"
data="$(mktemp -d /tmp/arcana-kind-XXXXXX)"
chmod 700 "$data"
work="$(mktemp -d)"
forward=""
fail() { echo "FAIL: $*" >&2; exit 1; }
cleanup() {
  [ -z "$forward" ] || kill "$forward" 2>/dev/null || true
  docker ps -aq --filter label=arcana.managed=true | xargs -r docker rm -f >/dev/null 2>&1 || true
  docker volume ls -q --filter label=arcana.managed=true | xargs -r docker volume rm -f >/dev/null 2>&1 || true
  docker network ls -q --filter label=arcana.managed=true | xargs -r docker network rm >/dev/null 2>&1 || true
  kind delete cluster --name "$cluster" >/dev/null 2>&1 || true
  rm -rf "$data" "$work"
}
trap cleanup EXIT

for tool in docker kind kubectl helm; do command -v "$tool" >/dev/null || fail "$tool is not on PATH"; done
for image in arcana:local arcana-base:local; do docker image inspect "$image" >/dev/null 2>&1 || fail "missing image $image (make proxy-image templates)"; done
[ -x "$root/bin/arcana" ] || fail "missing bin/arcana (make build)"

runtime=runc
docker info --format '{{json .Runtimes}}' | grep -q '"runsc"' && runtime=runsc
insecure=false
[ "$runtime" = runc ] && insecure=true
group="$(stat -c %g /var/run/docker.sock)"
echo "runtime=$runtime docker socket group=$group data=$data"

cat >"$work/kind.yaml" <<YAML
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
    extraMounts:
      - {hostPath: /var/run/docker.sock, containerPath: /var/run/docker.sock}
      - {hostPath: $data, containerPath: $data}
YAML
cat >"$work/values.yaml" <<YAML
nodeName: $cluster-control-plane
docker: {socketGroup: $group}
dataDir: $data
user: {uid: $(id -u), gid: $(id -g)}
image: {repository: arcana, tag: local, pullPolicy: Never}
tolerations:
  - {key: node-role.kubernetes.io/control-plane, operator: Exists, effect: NoSchedule}
config:
  runtime: {docker: {oci_runtime: $runtime, allow_insecure_runtime: $insecure}}
  templates:
    - {name: base, image: "arcana-base:local", warm_pool: 0}
YAML

kind create cluster --name "$cluster" --config "$work/kind.yaml" --wait 240s
kind load docker-image arcana:local --name "$cluster"
kubectl create namespace "$namespace"
kubectl label namespace "$namespace" pod-security.kubernetes.io/enforce=privileged
helm install arcana "$root/deploy/helm/arcana" -n "$namespace" -f "$work/values.yaml"
kubectl -n "$namespace" rollout status deploy/arcana --timeout=180s

forward_port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')"
connect() {
  [ -z "$forward" ] || kill "$forward" 2>/dev/null || true
  kubectl -n "$namespace" port-forward svc/arcana "$forward_port:7070" >/dev/null 2>&1 &
  forward=$!
  for _ in $(seq 1 30); do curl -fs "http://127.0.0.1:$forward_port/v1/health" >/dev/null 2>&1 && return 0; sleep 1; done
  fail "the API did not answer through the Service"
}
connect
export ARCANA_URL="http://127.0.0.1:$forward_port" ARCANA_API_KEY="$(cat "$data/admin.key")"
arcana="$root/bin/arcana"

echo "== a sandbox works through the pod"
id="$("$arcana" sandbox create --template base --json | python3 -c 'import sys,json; print(json.load(sys.stdin)["id"])')"
[ "$("$arcana" exec "$id" -- sh -c 'echo ok > /workspace/f; id -u; cat /workspace/f' | tr '\n' ' ')" = "1000 ok " ] || fail "exec in the sandbox"

echo "== helm test"
helm test arcana -n "$namespace" | grep -q 'Phase: *Succeeded' || fail "helm test"

echo "== the pod is not privileged"
kubectl -n "$namespace" get pod -o json >"$work/pod.json"
python3 - "$work/pod.json" <<'PY'
import json, sys
pod = json.load(open(sys.argv[1]))["items"][0]["spec"]
c = pod["containers"][0]["securityContext"]
assert not c.get("privileged") and c["readOnlyRootFilesystem"] and c["allowPrivilegeEscalation"] is False, c
assert c["capabilities"]["drop"] == ["ALL"], c
assert pod["securityContext"]["runAsNonRoot"] is True, pod["securityContext"]
PY

echo "== an upgrade replaces the pod; the sandbox, its workspace and the admin key survive"
old_pod="$(kubectl -n "$namespace" get pod -o name)"
helm upgrade arcana "$root/deploy/helm/arcana" -n "$namespace" -f "$work/values.yaml" --set config.limits.max_sandboxes=15
kubectl -n "$namespace" rollout status deploy/arcana --timeout=180s
[ "$old_pod" != "$(kubectl -n "$namespace" get pod -o name)" ] || fail "the pod was not replaced"
connect
[ "$ARCANA_API_KEY" = "$(cat "$data/admin.key")" ] || fail "the admin key changed"
[ "$("$arcana" exec "$id" -- cat /workspace/f)" = "ok" ] || fail "the sandbox lost its workspace across the upgrade"

echo "== uninstalling leaves the sandbox running and the data in place"
helm uninstall arcana -n "$namespace"
docker inspect -f '{{.State.Running}}' "sbx-$id" | grep -q true || fail "the sandbox stopped with the release"

echo "== a wrong Docker socket group is reported clearly"
helm install arcana "$root/deploy/helm/arcana" -n "$namespace" -f "$work/values.yaml" --set docker.socketGroup=1
for _ in $(seq 1 40); do kubectl -n "$namespace" logs deploy/arcana 2>&1 | grep -q 'permission denied' && break; sleep 2; done
kubectl -n "$namespace" logs deploy/arcana 2>&1 | grep -q 'permission denied' || fail "no clear error for a wrong socket group"
helm uninstall arcana -n "$namespace"

echo "kind e2e passed"
