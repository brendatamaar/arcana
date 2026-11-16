#!/usr/bin/env bash
# Run the containerd driver's network escape tests (ESC-15..19 through the public API) as root inside a
# throwaway containerd, with the CNI plugins and the egress proxy image.
#
#   make build proxy-image templates && scripts/containerd-escape.sh [go test args...]
#
# Allowlist networking creates network namespaces and runs CNI plugins, so the test binary runs as root inside the
# containerd container (see scripts/lib-containerd.sh). It reaches the real internet through the egress proxy.
set -euo pipefail
EXTRA_IMAGES="arcana:local arcana-python:local"
source "$(dirname "${BASH_SOURCE[0]}")/lib-containerd.sh"

cd "$root"
go test -c -tags escape -o "$dir/containerd-escape.test" ./test/escape/
docker exec -w "$root" \
  -e ARCANA_ESCAPE_CONTAINERD_ADDRESS="$dir/containerd.sock" -e ARCANA_ESCAPE_CNI_BIN_DIR=/opt/cni/bin \
  -e ARCANA_ESCAPE_PROXY_IMAGE=docker.io/library/arcana:local -e ARCANA_ESCAPE_PYTHON_IMAGE=docker.io/library/arcana-python:local \
  -e ARCANA_ESCAPE_RUNTIME="${ARCANA_ESCAPE_RUNTIME:-runsc}" \
  "$name" "$dir/containerd-escape.test" -test.count=1 -test.timeout=15m -test.v -test.run TestEscapeV2_ContainerdNetworkPublicAPI "$@"
