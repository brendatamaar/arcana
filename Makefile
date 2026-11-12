VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short HEAD)
BUILD_DATE ?= $(shell git log -1 --format=%cI)
LDFLAGS = -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(BUILD_DATE)

.PHONY: helper build test test-integration test-escape lint generate verify-generated check clean
build: export CGO_ENABLED = 0
build: helper
	go build -ldflags "$(LDFLAGS)" -o bin/arcana ./cmd/arcana

helper:
	go run ./scripts/buildhelper --bin

test:
	go test -race ./...

# Suites are introduced by their runtime tasks; until then these run the existing tests.
test-integration:
	go test -race -tags=integration ./...

test-escape:
	go test -race -tags=escape ./...

lint:
	golangci-lint run --allow-serial-runners

generate:
	go generate ./...

verify-generated: generate
	git diff --exit-code -- internal/api/gen internal/store/db internal/helper/assets

check: verify-generated lint test

clean:
	go clean ./...
	go run ./scripts/clean.go

.PHONY: templates
templates:
	@for name in base python node go; do docker build -t arcana-$$name:local templates/$$name || exit $$?; done

.PHONY: proxy-image
proxy-image: build
	docker build -f deploy/Dockerfile -t arcana:local .

# SDKs (Python and TypeScript). Generated code is committed; see docs/decisions/0005-sdk-generation.md.
PNPM ?= pnpm
PYTHON ?= python3

.PHONY: sdk-generate sdk-verify-generated sdk-test-typescript sdk-test-python sdk-test
sdk-generate:
	cd sdk/typescript && $(PNPM) install --frozen-lockfile && $(PNPM) run generate
	cd sdk/python && openapi-python-client generate --path ../../api/openapi.yaml --meta none --output-path src/arcana/_generated --overwrite

# Compares against the index, so stage generated files first when checking uncommitted work.
sdk-verify-generated: sdk-generate
	git diff --exit-code -- sdk/typescript/src/schema.d.ts sdk/python/src/arcana/_generated

# Unit tests run anywhere; the live tests start a throwaway Arcana (needs `make build` and `make templates`).
sdk-test-typescript: build
	cd sdk/typescript && $(PNPM) install --frozen-lockfile && $(PNPM) run typecheck && $(PNPM) test
	cd sdk/typescript && ../../scripts/with-arcana.sh $(PNPM) run test:live

sdk-test-python: build
	cd sdk/python && PYTHONPATH=src $(PYTHON) -W error -m unittest discover -s tests -t .
	cd sdk/python && PYTHONPATH=src ../../scripts/with-arcana.sh $(PYTHON) -W error -m unittest tests.test_live -v

sdk-test: sdk-test-typescript sdk-test-python

# Needs a running Arcana (ARCANA_URL, ARCANA_API_KEY), the python/node/go template images and an
# OpenAI-compatible model (FIXAGENT_BASE_URL, FIXAGENT_MODEL; Ollama by default). Prints a results table.
.PHONY: eval-agent
eval-agent:
	go run ./examples/fix-agent eval $(EVAL_ARGS)

# Dashboard (web/). Needs Node LTS and pnpm (`corepack enable`); nothing else in the repo does.
.PHONY: web web-dev web-generate web-check web-e2e build-all
web:
	cd web && $(PNPM) install --frozen-lockfile && $(PNPM) run build && $(PNPM) run size

web-dev:
	cd web && $(PNPM) install --frozen-lockfile && $(PNPM) run dev

web-generate:
	cd web && $(PNPM) install --frozen-lockfile && $(PNPM) run generate

# Compares generated types against the index, like the other verify targets.
web-check:
	cd web && $(PNPM) install --frozen-lockfile && $(PNPM) run lint && $(PNPM) run typecheck && $(PNPM) run format:check && $(PNPM) run test && $(PNPM) run verify-generated && $(PNPM) run build && $(PNPM) run size

build-all: web build

# Smoke test in a real browser against a throwaway Arcana with the dashboard embedded (make build-all first;
# `pnpm exec playwright install chromium` once).
web-e2e:
	cd web && ../scripts/with-arcana.sh $(PNPM) run e2e
