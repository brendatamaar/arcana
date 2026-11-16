#!/usr/bin/env bash
# Run the containerd driver's integration tests against a real containerd, without root.
#
#   make build templates && scripts/containerd-test.sh [go test args...]
#
# containerd runs inside a throwaway privileged container (the Docker group is root-equivalent, which is
# the access the Docker driver already needs). Its socket sits in a directory you own, and the host's
# gVisor files and the helper are mounted at the same paths. The kind node image provides containerd and
# runc. This is a stand-in for a real host: containerd is nested in a container, so timings include that.
# The setup lives in scripts/lib-containerd.sh.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib-containerd.sh"

# The egress proxy image and a Python template for the allowlist networking test.
for image in arcana:local arcana-python:local; do
  docker image inspect "$image" >/dev/null 2>&1 || fail "missing image $image (make proxy-image templates)"
  docker save "$image" | ctr images import - >/dev/null
done
export ARCANA_CONTAINERD_ADDRESS="$dir/containerd.sock" ARCANA_CONTAINERD_HELPER="$root/bin/sbx-exec" ARCANA_CONTAINERD_FIFO_DIR="$dir/fifo"
cd "$root"
go test -tags integration -count=1 -timeout 15m -v ./internal/runtime/containerd/ "$@"

# The volume workspace and allowlist networking need a root client (they give a directory to the sandbox user,
# create network namespaces and run CNI plugins), so those tests run as root inside the containerd container,
# with the same socket and shared directories. ROOT_TESTS overrides which ones.
go test -c -tags integration -o "$dir/containerd.test" ./internal/runtime/containerd/
mkdir -p "$dir/workspaces"
docker exec -w "$root" \
  -e ARCANA_CONTAINERD_ADDRESS="$ARCANA_CONTAINERD_ADDRESS" -e ARCANA_CONTAINERD_HELPER="$ARCANA_CONTAINERD_HELPER" \
  -e ARCANA_CONTAINERD_FIFO_DIR="$ARCANA_CONTAINERD_FIFO_DIR" -e ARCANA_CONTAINERD_WORKSPACE_DIR="$dir/workspaces" \
  -e ARCANA_CONTAINERD_CNI_BIN_DIR=/opt/cni/bin -e ARCANA_CONTAINERD_PROXY_IMAGE=docker.io/library/arcana:local \
  -e ARCANA_CONTAINERD_PYTHON_IMAGE=docker.io/library/arcana-python:local \
  "$name" "$dir/containerd.test" -test.count=1 -test.timeout=15m -test.v -test.run "${ROOT_TESTS:-Volume|Allowlist}"
