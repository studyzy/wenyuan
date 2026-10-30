# wenyuan — build and test entrypoints.
#
# The default build is standard-library-only and offline. The PostgreSQL
# driver path is compiled with the 'pgx' build tag ('make pgx').
# All Go commands run with GOFLAGS=-mod=mod and CGO disabled to keep the
# build reproducible across environments.

GO        ?= go
GOFLAGS   ?= -mod=mod
PKGS      ?= ./...
BINARY    ?= bin/wenyuan-server
BANKCTL   ?= bin/bankctl
WENYUANCLI ?= bin/wenyuan-admin
WORKER    ?= bin/worker
CMD       ?= ./cmd/server
CMDCTL    ?= ./cmd/bankctl
CMDCLI    ?= ./cmd/wenyuan
CMDWORKER ?= ./cmd/worker
GOLANGCI_LINT ?= golangci-lint
# The frontend's BFF proxies to the API on this URL (frontend/lib/server/config.ts).
WENYUAN_CP_DATAPLANE_API_URL ?= http://127.0.0.1:18888
# The Go client SDK's live suite talks to a server on this URL.
SDK_API_URL ?= http://127.0.0.1:18888

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

.PHONY: all build test vet pgx lint check fmt tidy clean run db-up tei-up db-down install \
        frontend-build frontend-run frontend-standalone clients clients-check \
        test-sdk test-sdk-integration test-sdk-go test-sdk-go-integration \
        test-sdk-python test-sdk-typescript test-sdk-rust help \
        bench test-pg cover cover-check cover-check-pg parity \
        service-install service-enable service-disable service-restart \
        service-stop service-start service-status service-uninstall service-logs

all: check

## parity: run the reference's own system-test stories against this service
# The reference ships 77 blackbox stories that drive a real server through the
# published Python client with the model, embedding model and reranker pointed at
# a stub. Running them against this build is the executable definition of
# "the same service"; see scripts/run_parity_suite.sh for what it needs
# (the reference checkout and its system-test virtualenv).
parity:
	scripts/run_parity_suite.sh $(PARITY_ARGS)

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

## run: run the API server (with the pgx driver, so a configured database_url works)
run:
	$(GO) run -tags pgx $(CMD) $(ARGS)

## db-up: start this project's own PostgreSQL via docker compose (host port 15432)
db-up:
	docker compose up -d postgres

## tei-up: start the TEI embedding/reranker sidecars from the compose tei profile
tei-up:
	docker compose --profile tei up -d tei-embedding tei-reranker

## db-down: stop the compose stack; data persists in the named volume
db-down:
	docker compose down

## install: build the deployment binaries into ./bin with the pgx tag, so the
## server, worker and bankctl reach PostgreSQL (wenyuan-admin is an API client
## and unaffected by the tag)
install:
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -tags pgx -o $(BINARY) $(CMD)
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -tags pgx -o $(WORKER) $(CMDWORKER)
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -tags pgx -o $(BANKCTL) $(CMDCTL)
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -tags pgx -o $(WENYUANCLI) $(CMDCLI)

## frontend-build: production build of the control-plane frontend (frontend/)
frontend-build:
	cd frontend && npm run build

## frontend-run: serve the frontend in production mode on port 19999.
##
## Run the standalone output, not 'next start': next.config.mjs sets
## 'output: "standalone"', and 'next start' refuses that configuration — it
## prints a warning and exits without ever listening. The standalone server is
## also the only thing that runs what a deployment runs.
##
## The standalone tree is not self-contained: 'next build' does not copy
## 'public/' or '.next/static' into it, so both are staged in first. Without
## them the app boots and serves HTML but every asset 404s.
##
## HOSTNAME is pinned to the IPv4 loopback: the standalone server resolves its
## own hostname when HOSTNAME is unset, and here that produced an IPv6-only
## '[::1]' bind, so 'http://127.0.0.1:19999' — the URL the BFF and the docs use —
## was unreachable.
##
## dev mode ('npm run dev') hangs Origin-bearing HMR WebSocket upgrades in this
## environment, which leaves the dashboard reloading forever on an empty list.
frontend-run: frontend-standalone
	cd frontend/.next/standalone && PORT=19999 HOSTNAME=127.0.0.1 \
		WENYUAN_CP_DATAPLANE_API_URL=$(WENYUAN_CP_DATAPLANE_API_URL) \
		node server.js

## frontend-standalone: stage the standalone output for 'frontend-run' —
## production build, then graft 'public/' and '.next/static' into the tree.
frontend-standalone:
	cd frontend && npm run build
	rm -rf frontend/.next/standalone/.next/static frontend/.next/standalone/public
	cp -r frontend/.next/static frontend/.next/standalone/.next/static
	cp -r frontend/public frontend/.next/standalone/public

## service-install: install this checkout as systemd services (backend and
## frontend) and enable them at boot. Needs sudo; the templates live in
## deploy/systemd/ and the checkout path and the account running them are
## substituted in.
##
## The frontend unit is only installed when a standalone build exists — it
## would restart-loop without one. /etc/default/wenyuan-frontend is seeded on
## first install and never overwritten, so the upstream URL and the API key set
## there survive a reinstall.
##
## Installing does not restart a running service; run 'make service-restart'
## to pick up a new binary or a new frontend build.
SYSTEMD_DIR     ?= /etc/systemd/system
SERVICE_ENV     ?= /etc/default/wenyuan-frontend
SERVICE_USER    ?= $(shell id -un)
SERVICE_ROOT    := $(CURDIR)
NODE            ?= $(shell command -v node)

define render_unit
sudo sed -e 's|__ROOT__|$(SERVICE_ROOT)|g' \
         -e 's|__USER__|$(SERVICE_USER)|g' \
         -e 's|__NODE__|$(NODE)|g' $(1) | sudo tee $(2) >/dev/null
endef

service-install:
	@test -n "$(NODE)" || { echo "node is required: it runs the frontend unit"; exit 1; }
	$(call render_unit,deploy/systemd/wenyuan.service,$(SYSTEMD_DIR)/wenyuan.service)
	@if [ -f frontend/.next/standalone/server.js ]; then \
		$(call render_unit,deploy/systemd/wenyuan-frontend.service,$(SYSTEMD_DIR)/wenyuan-frontend.service); \
		if [ ! -f $(SERVICE_ENV) ]; then \
			sudo install -m 600 deploy/systemd/wenyuan-frontend.env $(SERVICE_ENV); \
			echo "seeded $(SERVICE_ENV): set WENYUAN_CP_DATAPLANE_API_KEY there when the API requires one"; \
		fi; \
	else echo "skipping the frontend unit: run 'make frontend-standalone' first"; fi
	sudo systemctl daemon-reload

## service-enable: start both services now and at every boot
service-enable:
	sudo systemctl enable --now wenyuan.service
	@if [ -f $(SYSTEMD_DIR)/wenyuan-frontend.service ]; then sudo systemctl enable --now wenyuan-frontend.service; fi

## service-disable: stop both services and stop starting them at boot. The unit
## files stay installed; 'make service-uninstall' removes them.
service-disable:
	-sudo systemctl disable --now wenyuan-frontend.service
	sudo systemctl disable --now wenyuan.service

## service-restart: restart both, e.g. after 'make install' or
## 'make frontend-standalone'
service-restart:
	sudo systemctl restart wenyuan.service
	@if [ -f $(SYSTEMD_DIR)/wenyuan-frontend.service ]; then sudo systemctl restart wenyuan-frontend.service; fi

## service-stop: stop both without touching whether they start at boot
service-stop:
	-sudo systemctl stop wenyuan-frontend.service
	sudo systemctl stop wenyuan.service

## service-start: start both again
service-start:
	sudo systemctl start wenyuan.service
	@if [ -f $(SYSTEMD_DIR)/wenyuan-frontend.service ]; then sudo systemctl start wenyuan-frontend.service; fi

## service-status: state of both services plus a health probe of the API
service-status:
	-systemctl status wenyuan.service --no-pager -l
	-systemctl status wenyuan-frontend.service --no-pager -l
	@curl -s -m 5 http://127.0.0.1:18888/health || echo "API is not answering on 18888"

## service-uninstall: disable and remove the unit files. The database, the
## build output and the logs are left alone.
service-uninstall:
	-sudo systemctl disable --now wenyuan-frontend.service wenyuan.service
	sudo rm -f $(SYSTEMD_DIR)/wenyuan-frontend.service $(SYSTEMD_DIR)/wenyuan.service
	sudo systemctl daemon-reload

## service-logs: follow the API log; 'make service-logs UNIT=frontend' for the
## frontend log
UNIT ?= server
service-logs:
	tail -f /var/log/wenyuan-$(UNIT).log

## clean: remove build output
clean:
	rm -rf bin

## clients: regenerate every client SDK under clients/ from the served contract
## (needs docker, node and cargo; each generator is pinned)
clients:
	scripts/generate_clients.sh

## clients-check: fail when clients/openapi.json no longer matches the served
## document's derived form
clients-check:
	python3 scripts/derive_client_spec.py --check

## test-sdk-go-integration: run the Go client SDK's live suite against a
## running server. Point SDK_API_URL at it; a memory-backed one is enough:
##   WENYUAN_API_DATABASE_URL=memory make run    # in another shell
test-sdk-go-integration:
	cd clients/go && WENYUAN_API_URL="$(SDK_API_URL)" $(GO) test -tags integration -count=1 $(GOFLAGS) ./...

## test-sdk: run every client SDK's offline suite (no server, no network)
test-sdk: test-sdk-go test-sdk-python test-sdk-typescript test-sdk-rust

## test-sdk-go: the Go SDK's offline suite (httptest servers only)
test-sdk-go:
	cd clients/go && $(GO) test -count=1 $(GOFLAGS) ./...

## test-sdk-python: the Python SDK's offline suite (stub servers only)
test-sdk-python:
	cd clients/python && python3 -m unittest discover -s tests

## test-sdk-typescript: the TypeScript SDK's type-check and offline suite
test-sdk-typescript:
	cd clients/typescript && npx tsc --noEmit && npm test

## test-sdk-rust: the Rust SDK's offline suite (stub servers only)
test-sdk-rust:
	cd clients/rust && cargo test

## test-sdk-integration: run every client SDK's live suite against a running
## server (SDK_API_URL, default http://127.0.0.1:18888). Each language's live
## tests are gated on WENYUAN_API_URL, so a suite that finds no server is
## skipped rather than failed — start one with
##   WENYUAN_API_DATABASE_URL=memory make run    # in another shell
test-sdk-integration: test-sdk-go-integration
	cd clients/python && WENYUAN_API_URL="$(SDK_API_URL)" python3 -m unittest discover -s tests
	cd clients/typescript && WENYUAN_API_URL="$(SDK_API_URL)" npm test
	cd clients/rust && WENYUAN_API_URL="$(SDK_API_URL)" cargo test

## help: list targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## //'