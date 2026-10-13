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

# The PostgreSQL-backed suite needs a live instance and is therefore opt-in:
# the default gate stays offline. Point WENYUAN_TEST_DATABASE_URL at a database
# this project owns; the suite creates its own schema and cleans up.
PG_TEST_URL ?= $(WENYUAN_TEST_DATABASE_URL)
# The coverage floor sits just below the measured baseline so a regression fails
# the gate; raise it as coverage improves rather than leaving it aspirational.
# The default suite reaches ~58% of statements; the PostgreSQL-backed suite
# reaches ~68% because it also exercises the postgres backend.
COVERAGE_MIN ?= 56
# COVERAGE_MIN_PG is the floor for the PostgreSQL-backed suite.
COVERAGE_MIN_PG ?= 66

.PHONY: all build test vet pgx lint check fmt tidy clean run install help \
        bench test-pg cover cover-check cover-check-pg

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

## bench: run the benchmarks
bench:
	$(GO) test -run '^$$' -bench . -benchmem $(GOFLAGS) $(PKGS)

## test-pg: run the PostgreSQL-backed suite against a live instance
test-pg:
	@if [ -z "$(PG_TEST_URL)" ]; then \
		echo "test-pg needs WENYUAN_TEST_DATABASE_URL (or PG_TEST_URL) pointed at a database this project owns"; \
		exit 1; \
	fi
	WENYUAN_TEST_DATABASE_URL="$(PG_TEST_URL)" $(GO) test -tags pgx -count=1 $(GOFLAGS) $(PKGS)

## cover: report statement coverage
cover:
	$(GO) test -count=1 -coverprofile=coverage.out $(GOFLAGS) $(PKGS)
	$(GO) tool cover -func=coverage.out | tail -1

## cover-check: fail when statement coverage is below COVERAGE_MIN
cover-check:
	$(GO) test -count=1 -coverprofile=coverage.out $(GOFLAGS) $(PKGS)
	@total=$$($(GO) tool cover -func=coverage.out | tail -1 | sed 's/.*[[:space:]]\([0-9.]*\)%$$/\1/'); \
	echo "coverage: $$total% (minimum $(COVERAGE_MIN)%)"; \
	awk -v got="$$total" -v min="$(COVERAGE_MIN)" 'BEGIN { if (got+0 < min+0) { print "coverage below the minimum"; exit 1 } }'

## cover-check-pg: fail when the PostgreSQL-backed suite's coverage is below COVERAGE_MIN_PG
cover-check-pg:
	@if [ -z "$(PG_TEST_URL)" ]; then \
		echo "cover-check-pg needs WENYUAN_TEST_DATABASE_URL (or PG_TEST_URL) pointed at a database this project owns"; \
		exit 1; \
	fi
	WENYUAN_TEST_DATABASE_URL="$(PG_TEST_URL)" $(GO) test -tags pgx -count=1 -coverprofile=coverage-pg.out $(GOFLAGS) $(PKGS)
	@total=$$($(GO) tool cover -func=coverage-pg.out | tail -1 | sed 's/.*[[:space:]]\([0-9.]*\)%$$/\1/'); \
	echo "coverage (pgx): $$total% (minimum $(COVERAGE_MIN_PG)%)"; \
	awk -v got="$$total" -v min="$(COVERAGE_MIN_PG)" 'BEGIN { if (got+0 < min+0) { print "coverage below the minimum"; exit 1 } }'

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