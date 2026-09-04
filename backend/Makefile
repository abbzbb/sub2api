.PHONY: build generate lint test test-unit test-integration test-e2e

VERSION ?= $(shell ./scripts/resolve-version.sh)
LDFLAGS ?= -s -w -X main.Version=$(VERSION)

# Keep in sync with .github/workflows/backend-ci.yml (golangci-lint-action version).
# Run through `go run` so the linter is built with the toolchain go.mod requires;
# a distro-packaged binary built with an older Go cannot load this module.
GOLANGCI_LINT_VERSION ?= v2.13.1
GOLANGCI_LINT = GOTOOLCHAIN=auto GOFLAGS=-mod=mod go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

lint:
	$(GOLANGCI_LINT) run --timeout=30m ./...

build:
	CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -trimpath -o bin/server ./cmd/server

generate:
	go generate ./ent
	go generate ./cmd/server

test:
	go test ./...
	$(GOLANGCI_LINT) run --timeout=30m ./...

test-unit:
	go test -tags=unit ./...

test-integration:
	go test -tags=integration ./...

test-e2e:
	./scripts/e2e-test.sh

test-e2e-local:
	go test -tags=e2e -v -timeout=300s ./internal/integration/...
