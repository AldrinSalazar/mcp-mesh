# Build and check mcp-mesh. `make` builds the dashboard and both binaries
# into bin/; `make check` runs what a change should pass.

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
GO      ?= go
NPM     ?= npm

.PHONY: all build web hub node test test-go test-web test-browser vet fmt check livetest e2e-vm clean

all: build

## build: the dashboard, then bin/mcp-mesh-hub (which embeds it) and bin/mcp-mesh-node
build: web hub node

web:
	$(NPM) --prefix web ci
	$(NPM) --prefix web run build

hub:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/ ./cmd/mcp-mesh-hub

node:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/ ./cmd/mcp-mesh-node

## test: Go (with the race detector) and the dashboard's unit tests
test: test-go test-web

test-go:
	$(GO) test -race ./...

test-web:
	$(NPM) --prefix web run typecheck
	$(NPM) --prefix web test

## test-browser: the dashboard in Chromium against its mock API (Playwright)
test-browser:
	$(NPM) --prefix web run test:e2e

## vet: go vet for every supported platform, and gofmt
vet:
	GOOS=linux $(GO) vet ./...
	GOOS=darwin $(GO) vet ./...
	GOOS=windows $(GO) vet ./...
	@test -z "$$(gofmt -l cmd internal tools e2e)" || { gofmt -l cmd internal tools e2e; echo "gofmt needed"; exit 1; }

fmt:
	gofmt -w cmd internal tools e2e

## check: what a change should pass before it is sent
check: vet test livetest

## livetest: the real binaries on one machine (fixed ports 7100-7102, 9123)
livetest:
	tools/livetest/livetest.sh

## e2e-vm: scenarios on Hyper-V VMs from WSL (see e2e/README.md; needs MCPMESH_LAB_DIR)
e2e-vm:
	$(GO) test -tags e2e -timeout 60m ./e2e/

clean:
	rm -rf bin e2e-results
