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
	golangci-lint run

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
