#!/usr/bin/env bash
# Run the containerd driver's integration tests against a real containerd, without root.
#
#   make build templates && scripts/containerd-test.sh [go test args...]
#
# containerd runs inside a throwaway privileged container (the Docker group is root-equivalent, which is
# the access the Docker driver already needs). Its socket sits in a directory you own, and the host's
# gVisor files and the helper are mounted at the same paths. The kind node image provides containerd and
# runc. This is a stand-in for a real host: containerd is nested in a container, so timings include that.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
node="${CONTAINERD_IMAGE:-kindest/node:v1.35.0}"
name="arcana-containerd-test"
dir="$(mktemp -d /tmp/arcana-ctd-XXXXXX)"
volume="$name-root"
fail() { echo "FAIL: $*" >&2; exit 1; }
cleanup() {
  docker rm -f "$name" >/dev/null 2>&1 || true
  docker volume rm -f "$volume" >/dev/null 2>&1 || true
  rm -rf "$dir"
}
trap cleanup EXIT

command -v docker >/dev/null || fail "docker is not on PATH"
docker image inspect arcana-base:local >/dev/null 2>&1 || fail "missing image arcana-base:local (make templates)"
docker image inspect "$node" >/dev/null 2>&1 || docker pull "$node"
[ -x "$root/bin/sbx-exec" ] || fail "missing bin/sbx-exec (make build)"

cat >"$dir/containerd.toml" <<TOML
version = 3
root = "/var/lib/containerd"
state = "/run/containerd-state"
[grpc]
  address = "$dir/containerd.sock"
  uid = $(id -u)
  gid = $(id -g)
TOML

mounts=(-v "$dir:$dir" -v "$volume:/var/lib/containerd" -v "$root/bin:$root/bin:ro")
# gVisor is optional: without it only the runc subtests run, and the gVisor ones fail on purpose.
if command -v runsc >/dev/null; then
  gvisor_dir="$(dirname "$(readlink -f "$(command -v runsc)")")"
  mounts+=(-v "$gvisor_dir/runsc:/usr/local/bin/runsc:ro" -v "$gvisor_dir/containerd-shim-runsc-v1:/usr/local/bin/containerd-shim-runsc-v1:ro")
  [ ! -d "$gvisor_dir/gvisor-bin" ] || mounts+=(-v "$gvisor_dir/gvisor-bin:/usr/local/bin/gvisor-bin:ro")
  [ ! -f /etc/containerd/runsc.toml ] || mounts+=(-v /etc/containerd/runsc.toml:/etc/containerd/runsc.toml:ro)
fi
# CONTAINERD_BIN_DIR: a directory with containerd and ctr binaries to use instead of the image's, for
# testing against another containerd version (for example the one matching the client library).
if [ -n "${CONTAINERD_BIN_DIR:-}" ]; then
  mounts+=(-v "$CONTAINERD_BIN_DIR/containerd:/usr/local/bin/containerd:ro" -v "$CONTAINERD_BIN_DIR/ctr:/usr/local/bin/ctr:ro")
fi
# cgroup v2 forbids a cgroup from holding processes and delegating controllers at once, so containerd
# first moves into a child cgroup and the controllers are enabled on the container's own cgroup (the same
# step Docker-in-Docker images take). A private cgroup namespace matters: with the host's, gVisor
# containers took 5 s to start in this setup.
docker run -d --name "$name" --privileged --entrypoint sh "${mounts[@]}" "$node" -c '
  set -e
  mkdir -p /sys/fs/cgroup/init && echo $$ > /sys/fs/cgroup/init/cgroup.procs
  sed -e "s/ / +/g" -e "s/^/+/" /sys/fs/cgroup/cgroup.controllers > /sys/fs/cgroup/cgroup.subtree_control
  exec containerd --config "$0"' "$dir/containerd.toml" >/dev/null
for _ in $(seq 1 30); do [ -S "$dir/containerd.sock" ] && break; sleep 1; done
[ -S "$dir/containerd.sock" ] || fail "containerd did not start: $(docker logs "$name" 2>&1 | tail -3)"

ctr() { docker exec -i "$name" ctr -a "$dir/containerd.sock" -n arcana "$@"; }
docker save arcana-base:local | ctr images import - >/dev/null

export ARCANA_CONTAINERD_ADDRESS="$dir/containerd.sock" ARCANA_CONTAINERD_HELPER="$root/bin/sbx-exec" ARCANA_CONTAINERD_FIFO_DIR="$dir/fifo"
cd "$root"
go test -tags integration -count=1 -timeout 15m -v ./internal/runtime/containerd/ "$@"
