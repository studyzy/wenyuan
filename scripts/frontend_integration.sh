#!/usr/bin/env bash
# End-to-end integration check: the frontend BFF in front of this repository's
# own service.
#
#   scripts/frontend_integration.sh
#
# What it proves, in order:
#   1. the Go service starts on a private database and migrates it;
#   2. the frontend builds and serves, with its dataplane pointed at that service;
#   3. the access gate refuses an unauthenticated API call and admits one after
#      the login route, so the frontend is not silently open;
#   4. a bank is created on the service and the frontend's own API routes read it
#      back — the BFF is talking to this service, not to a mock;
#   5. a memory written *through the frontend's route* is visible through the
#      frontend's read routes, which is the write path a user actually takes;
#   6. the page routes render server-side HTML.
#
# It fails on the first mismatch and prints the offending body, so it doubles as
# a hand check. Everything it starts, it stops.
set -u

ROOT=$(cd "$(dirname "$0")/.." && pwd)
FRONTEND="$ROOT/frontend"

DATAPLANE_PORT=${DATAPLANE_PORT:-18080}
FRONTEND_PORT=${FRONTEND_PORT:-19999}
LOCALE=${LOCALE:-en}
ACCESS_KEY=${ACCESS_KEY:-integration-key}
DB_URL=${FRONTEND_IT_DB_URL:-}
BIN=${FRONTEND_IT_BIN:-}
KEEP=${FRONTEND_IT_KEEP:-}
SKIP_BUILD=${FRONTEND_IT_SKIP_BUILD:-}

DATAPLANE="http://127.0.0.1:${DATAPLANE_PORT}"
FRONTEND_URL="http://127.0.0.1:${FRONTEND_PORT}"
COOKIE_JAR=$(mktemp -t frontend-it-cookies-XXXXXX)
WORK=$(mktemp -d -t frontend-it-XXXXXX)
FAILURES=0
PIDS=()

cleanup() {
  for pid in "${PIDS[@]:-}"; do
    kill -- "-$pid" 2>/dev/null || kill "$pid" 2>/dev/null || true
  done
  # A process group that ignored the signal (or a server started outside this run)
  # would leave the port taken and answer the *next* run from a stale build.
  for port in "$FRONTEND_PORT" "$DATAPLANE_PORT"; do
    if command -v fuser >/dev/null 2>&1; then fuser -k "${port}/tcp" >/dev/null 2>&1 || true; fi
  done
  if [ -z "$KEEP" ]; then
    if [ -n "${CREATED_DB:-}" ]; then
      PGPASSWORD=${PGPASSWORD:-hindsight} psql -h "${PGHOST:-127.0.0.1}" -p "${PGPORT:-5432}" \
        -U "${PGUSER:-hindsight}" -c "DROP DATABASE IF EXISTS $CREATED_DB" postgres >/dev/null 2>&1 || true
    fi
    rm -rf "$WORK" "$COOKIE_JAR"
  else
    echo "kept: $WORK (cookies $COOKIE_JAR)"
  fi
}
trap cleanup EXIT

check() {
  local name=$1 want=$2 got=$3 extra=${4:-}
  if [ "$want" = "$got" ]; then
    echo "ok   ${name}"
  else
    echo "FAIL ${name}: want ${want}, got ${got}${extra:+ — $extra}"
    FAILURES=$((FAILURES + 1))
  fi
}

check_contains() {
  local name=$1 needle=$2 haystack=$3
  case "$haystack" in
    *"$needle"*) echo "ok   ${name}" ;;
    *)
      echo "FAIL ${name}: body does not contain ${needle}"
      echo "     body: $(printf '%s' "$haystack" | head -c 400)"
      FAILURES=$((FAILURES + 1))
      ;;
  esac
}

# A body that came back as a redirect/login page rather than JSON is the most
# common way an integration check lies, so the callers assert the status first
# and this only shapes the output.
body() { printf '%s' "$1" | head -c 600; }

# ---------------------------------------------------------------------------
# 1. the service
# ---------------------------------------------------------------------------
if [ -z "$DB_URL" ]; then
  CREATED_DB="wenyuan_frontend_it_$$"
  PGPASSWORD=${PGPASSWORD:-hindsight} psql -h "${PGHOST:-127.0.0.1}" -p "${PGPORT:-5432}" \
    -U "${PGUSER:-hindsight}" -c "CREATE DATABASE $CREATED_DB" postgres >/dev/null 2>&1 || {
    echo "could not create $CREATED_DB; set FRONTEND_IT_DB_URL to a database to use" >&2
    exit 2
  }
  DB_URL="postgresql://${PGUSER:-hindsight}:${PGPASSWORD:-hindsight}@${PGHOST:-127.0.0.1}:${PGPORT:-5432}/$CREATED_DB"
fi

if [ -z "$BIN" ]; then
  BIN="$WORK/wenyuan-server"
  (cd "$ROOT" && go build -tags pgx -o "$BIN" ./cmd/server) || {
    echo "the service did not build" >&2
    exit 2
  }
fi

setsid env HINDSIGHT_API_DATABASE_URL="$DB_URL" \
  HINDSIGHT_API_RUN_MIGRATIONS_ON_STARTUP=true \
  HINDSIGHT_API_PORT=$DATAPLANE_PORT \
  "$BIN" >"$WORK/dataplane.log" 2>&1 &
PIDS+=($!)

for _ in $(seq 1 60); do
  curl -fsS -o /dev/null "$DATAPLANE/health/live" 2>/dev/null && break
  sleep 0.5
done
if ! curl -fsS -o /dev/null "$DATAPLANE/health/live" 2>/dev/null; then
  echo "the service did not become ready; log:" >&2
  tail -20 "$WORK/dataplane.log" >&2
  exit 1
fi

# ---------------------------------------------------------------------------
# 2. the frontend
# ---------------------------------------------------------------------------
if [ -z "$SKIP_BUILD" ]; then
  (cd "$FRONTEND" && npm run build >"$WORK/frontend-build.log" 2>&1) || {
    echo "the frontend did not build; tail:" >&2
    tail -30 "$WORK/frontend-build.log" >&2
    exit 2
  }
fi

# setsid gives the frontend its own process group: `npm run start` spawns the
# Next server as a child, and killing only npm leaves the server holding the port.
(cd "$FRONTEND" && setsid env \
  HINDSIGHT_CP_DATAPLANE_API_URL="$DATAPLANE" \
  HINDSIGHT_CP_ACCESS_KEY="$ACCESS_KEY" \
  PORT=$FRONTEND_PORT \
  npm run start >"$WORK/frontend.log" 2>&1) &
PIDS+=($!)

for _ in $(seq 1 60); do
  curl -fsS -o /dev/null "$FRONTEND_URL/$LOCALE" 2>/dev/null && break
  sleep 0.5
done

# ---------------------------------------------------------------------------
# 3. the access gate
# ---------------------------------------------------------------------------
gate=$(curl -sS -o /dev/null -w '%{http_code}' "$FRONTEND_URL/api/banks")
check "an unauthenticated API call is refused" 401 "$gate"

login=$(curl -sS -X POST -H 'content-type: application/json' \
  -d "{\"key\":\"$ACCESS_KEY\"}" -c "$COOKIE_JAR" \
  -o /dev/null -w '%{http_code}' "$FRONTEND_URL/api/auth/login")
check "the login route admits the access key" 200 "$login"

wrong=$(curl -sS -X POST -H 'content-type: application/json' \
  -d '{"key":"not-the-key"}' -o /dev/null -w '%{http_code}' \
  "$FRONTEND_URL/api/auth/login")
check "the login route refuses a wrong key" 401 "$wrong"

auth() { curl -sS -b "$COOKIE_JAR" "$@"; }

# ---------------------------------------------------------------------------
# 4. the BFF reads the service
# ---------------------------------------------------------------------------
bank="frontend-it-$$"
create=$(curl -sS -X PUT -H 'content-type: application/json' \
  -d '{"name":"Frontend integration bank"}' -o /dev/null -w '%{http_code}' \
  "$DATAPLANE/v1/default/banks/$bank")
check "the service creates a bank" 200 "$create"

list=$(auth -o "$WORK/list.json" -w '%{http_code}' "$FRONTEND_URL/api/banks?limit=100")
check "the frontend lists banks through the BFF" 200 "$list"
check_contains "the created bank is in the frontend's list" "$bank" "$(cat "$WORK/list.json")"

# A route that reads the bank itself proves the id travels through, not just the
# collection endpoint.
config=$(auth -o "$WORK/config.json" -w '%{http_code}' "$FRONTEND_URL/api/banks/$bank/config")
check "the frontend reads the bank's config through the BFF" 200 "$config"

# ---------------------------------------------------------------------------
# 5. write through the frontend, read back through the frontend
# ---------------------------------------------------------------------------
retain=$(auth -X POST -H 'content-type: application/json' \
  -d "{\"bank_id\":\"$bank\",\"items\":[{\"content\":\"The integration check writes this sentence.\"}]}" \
  -o "$WORK/retain.json" -w '%{http_code}' "$FRONTEND_URL/api/memories/retain")
check "a memory can be written through the frontend's route" 200 "$retain"
check_contains "the write reports success" '"success":true' "$(cat "$WORK/retain.json" 2>/dev/null || true)"

memories=$(auth -o "$WORK/memories.json" -w '%{http_code}' \
  "$FRONTEND_URL/api/list?bank_id=$bank&limit=50")
check "the frontend reads memories back through the BFF" 200 "$memories"
check_contains "the written sentence is visible through the frontend" "integration check writes" \
  "$(cat "$WORK/memories.json" 2>/dev/null || true)"

# ---------------------------------------------------------------------------
# 6. the read surfaces the views depend on
# ---------------------------------------------------------------------------
for route in \
  "api/list?bank_id=$bank&limit=10" \
  "api/entities?bank_id=$bank" \
  "api/entities/graph?bank_id=$bank" \
  "api/stats/$bank" \
  "api/operations/$bank" \
  "api/documents?bank_id=$bank" \
  "api/knowledge-base/tree?bank_id=$bank" \
  "api/version"
do
  code=$(auth -o /dev/null -w '%{http_code}' "$FRONTEND_URL/$route")
  check "GET /$route" 200 "$code"
done

# Recall is a POST with a body, as it is in the reference client; a GET would only
# prove the router refuses it.
recall=$(auth -X POST -H 'content-type: application/json' \
  -d "{\"bank_id\":\"$bank\",\"query\":\"integration check writes\",\"limit\":5}" \
  -o "$WORK/recall.json" -w '%{http_code}' "$FRONTEND_URL/api/recall")
check "POST /api/recall" 200 "$recall"
check_contains "recall reaches the service and matches the retained sentence" \
  "integration check writes" "$(cat "$WORK/recall.json" 2>/dev/null || true)"

# ---------------------------------------------------------------------------
# 7. the pages render server-side
# ---------------------------------------------------------------------------
# The locale is never a URL segment in this app (next-intl is configured with
# localePrefix "never"), so the clean path is the contract and a prefixed path
# redirects to it. Both are asserted: the pages render, and the redirect keeps the
# prefixed form working.
root=$(auth -o /dev/null -w '%{http_code}' "$FRONTEND_URL/")
check "GET / redirects to the dashboard" 307 "$root"
rootLocation=$(auth -o /dev/null -D - -w '' "$FRONTEND_URL/" | tr -d '\r' | awk 'tolower($1)=="location:"{print $2}')
check "the root redirect names the dashboard" "/dashboard" "$rootLocation"

for page in "/banks/$bank" "/dashboard"; do
  code=$(auth -o "$WORK/page.html" -w '%{http_code}' "$FRONTEND_URL$page")
  check "GET $page" 200 "$code"
  check_contains "GET $page returns a document" "<html" "$(cat "$WORK/page.html" 2>/dev/null || true)"
done

prefix=$(auth -o /dev/null -w '%{http_code}' "$FRONTEND_URL/$LOCALE")
check "GET /$LOCALE redirects to the clean path" 307 "$prefix"
location=$(auth -o /dev/null -D - -w '' "$FRONTEND_URL/$LOCALE" | tr -d '\r' | awk 'tolower($1)=="location:"{print $2}')
check "the redirect drops the locale segment" "/" "$location"

echo
if [ "$FAILURES" -eq 0 ]; then
  echo "frontend integration: all checks passed"
  exit 0
fi
echo "frontend integration: $FAILURES check(s) failed"
echo "frontend log: $WORK/frontend.log"
tail -20 "$WORK/frontend.log"
exit 1