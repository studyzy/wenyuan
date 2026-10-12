# wenyuan — build and test entrypoints.
#
# The default build is standard-library-only and offline. The PostgreSQL
# driver path is compiled with the `pgx` build tag (`make pgx`).
# All Go commands run with GOFLAGS=-mod=mod and CGO disabled to keep the
# build reproducible across environments.

GO        ?= go
GOFLAGS   ?= -mod=mod
PKGS      ?= ./...
BINARY    ?= bin/wenyuan
BANKCTL   ?= bin/bankctl
CMD       ?= ./cmd/server
CMDCTL    ?= ./cmd/bankctl
GOLANGCI_LINT ?= golangci-lint

.PHONY: all build test vet pgx lint check fmt tidy clean run install help

all: check

## build: compile all packages (default build, no pgx)
build:
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) $(PKGS)

## test: run the full test suite
test:
	$(GO) test -count=1 $(GOFLAGS) $(PKGS)

## vet: static analysis of all packages
vet:
	$(GO) vet $(GOFLAGS) $(PKGS)

## pgx: compile the PostgreSQL driver path
pgx:
	CGO_ENABLED=0 $(GO) build -tags pgx $(GOFLAGS) $(PKGS)

## lint: run golangci-lint (config in .golangci.yml)
lint:
	$(GOLANGCI_LINT) run $(PKGS)

## check: the full verification gate (build, test, vet, pgx, lint)
check: build test vet pgx lint

## fmt: format all Go source
fmt:
	$(GO) fmt $(PKGS)

## tidy: synchronize go.mod / go.sum
tidy:
	$(GO) mod tidy

## run: run the API server
run:
	$(GO) run $(CMD) $(ARGS)

## install: build the server and admin binaries into ./bin
install:
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -o $(BINARY) $(CMD)
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -o $(BANKCTL) $(CMDCTL)

## clean: remove build output
clean:
	rm -rf bin

## help: list targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## //'