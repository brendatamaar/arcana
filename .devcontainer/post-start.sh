#!/usr/bin/env bash
# Runs at every start: make sure the nested Docker daemon is up and repeat the runtime banner.
set -uo pipefail
for _ in $(seq 1 30); do docker info > /dev/null 2>&1 && break; sleep 1; done
home="${ARCANA_DEV_HOME:-$HOME/.config/arcana}"
if [ -f "$home/WARNING" ]; then
  cat "$home/WARNING" >&2
else
  echo "Arcana dev runtime: $(cat "$home/RUNTIME" 2> /dev/null || echo unknown)" >&2
fi
