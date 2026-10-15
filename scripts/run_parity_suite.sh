#!/usr/bin/env bash
# Run the reference's own system-test stories against this service.
#
# The reference ships 77 blackbox stories (`hindsight-system-tests`) that drive a
# real server through the published Python client with the model, the embedding
# model and the reranker pointed at an in-process stub. They are the closest
# thing to an executable definition of "the same service", so this script wires
# them to the binary this repository builds.
#
# What it needs:
#   - the suite itself, copied verbatim into tests/system (fallback: the
#     reference checkout), plus the reference's system-test virtualenv, which
#     holds the published Python client the stories drive;
#   - a PostgreSQL the service may migrate and use. The suite asks for its
#     embedded instance; resolving that URL is done here with the reference's own
#     helper, so the service and the stories share one database.
#
# Usage:
#   scripts/run_parity_suite.sh                     # the whole suite
#   scripts/run_parity_suite.sh tests/test_01_retain_and_recall.py
#   PARITY_DB_URL=postgresql://user@host/db scripts/run_parity_suite.sh -k recall
#
# Environment overrides:
#   HINDSIGHT_REPO     the reference checkout (default /code/hindsight)
#   PARITY_DB_URL      a postgresql:// URL to use instead of resolving pg0
#   PG0_URL            the pg0 URL to resolve (default pg0://hindsight-systest:15499)
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
HINDSIGHT_REPO=${HINDSIGHT_REPO:-/code/hindsight}
# The suite is copied verbatim into tests/system; fall back to the reference
# checkout when the copy is absent. The virtualenv always comes from the
# reference checkout: it holds the editable hindsight-client the stories drive,
# which the copy deliberately does not vendor.
SUITE=${PARITY_SUITE:-$ROOT/tests/system}
UPSTREAM_SUITE="$HINDSIGHT_REPO/hindsight-system-tests"
if [ ! -d "$SUITE/tests" ]; then
  SUITE="$UPSTREAM_SUITE"
fi
PG0_URL=${PG0_URL:-pg0://hindsight-systest:15499}

if [ ! -d "$SUITE/tests" ]; then
  echo "the system tests are not at $SUITE; set PARITY_SUITE or HINDSIGHT_REPO" >&2
  exit 2
fi

PYTHON="$UPSTREAM_SUITE/.venv/bin/python"
if [ ! -x "$PYTHON" ]; then
  echo "the suite's virtualenv is missing at $UPSTREAM_SUITE/.venv; build it with uv sync" >&2
  exit 2
fi

# The binary under test.
BIN=${PARITY_BIN:-}
if [ -z "$BIN" ]; then
  BIN=$(mktemp -t wenyuan-parity-XXXXXX)
  # The suite's stories use a real PostgreSQL, so the driver must be compiled in:
  # the default build has no database driver and exits at startup.
  (cd "$ROOT" && go build -tags pgx -o "$BIN" ./cmd/server)
fi

# A database the stories and the service share. An explicit URL wins — either
# this script's own PARITY_DB_URL or a WENYUAN_DATABASE_URL the caller already
# set — because two suites sharing one database claim each other's queued work,
# and silently overriding a caller's private database made that mistake easy.
DB_URL=${PARITY_DB_URL:-${WENYUAN_DATABASE_URL:-}}
if [ -z "$DB_URL" ]; then
  if [ ! -x "$HINDSIGHT_REPO/.venv/bin/python" ]; then
    echo "set PARITY_DB_URL, or provide the reference venv so pg0 can be resolved" >&2
    exit 2
  fi
  DB_URL=$("$HINDSIGHT_REPO/.venv/bin/python" -c "
import asyncio, sys
sys.path.insert(0, '$HINDSIGHT_REPO/hindsight-api-slim')
from hindsight_api.pg0 import resolve_database_url
print(asyncio.run(resolve_database_url('$PG0_URL')))
")
fi

echo "binary:   $BIN"
echo "database: $DB_URL"
echo "suite:    $SUITE"

if [ "$#" -eq 0 ]; then
  set -- tests
fi

WENYUAN_BIN="$BIN" \
WENYUAN_DATABASE_URL="$DB_URL" \
PYTHONPATH="$ROOT/scripts:$SUITE${PYTHONPATH:+:$PYTHONPATH}" \
  "$PYTHON" -m pytest "$SUITE/$1" "${@:2}" -p system_parity_plugin