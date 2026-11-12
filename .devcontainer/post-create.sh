#!/usr/bin/env bash
# Runs once when the dev container is created.
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

echo "== toolchains" >&2
go version
node --version
sudo corepack enable pnpm 2> /dev/null || corepack enable pnpm || npm install -g pnpm

echo "== Go dependencies and linter" >&2
go mod download
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0 || echo "golangci-lint install failed; run it later" >&2

echo "== dashboard dependencies" >&2
(cd web && pnpm install --frozen-lockfile) || echo "web install failed; run 'make web-generate' later" >&2

echo "== sandbox runtime" >&2
bash .devcontainer/setup-runtime.sh

echo "== base template image" >&2
docker build -t arcana-base:local templates/base || echo "template build failed; run 'make templates' later" >&2

# Show the runtime banner in every new terminal so an insecure fallback is never silent.
if ! grep -q "arcana/WARNING" "$HOME/.bashrc" 2> /dev/null; then
  cat >> "$HOME/.bashrc" << 'RC'
[ -f "$HOME/.config/arcana/WARNING" ] && cat "$HOME/.config/arcana/WARNING"
RC
fi
echo "== done. Next: make build-all && ./bin/arcana serve --config ~/.config/arcana/dev.yaml" >&2
