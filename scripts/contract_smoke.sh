#!/usr/bin/env bash
# Smoke-probes the reference-contract claims that the audit found broken or
# newly implemented. It runs the built server and fails on the first mismatch,
# so it is usable both as a hand check and as the post-change proof.
#
#   scripts/contract_smoke.sh /path/to/server-binary
#
# Every probe asserts an observable wire fact: status code, header, or JSON
# body. It writes nothing outside a temp dir.
#
# **Give it a database nothing else has used.** Two probes depend on the bank
# being empty (a timeseries bucket counts the one memory this run writes, and a
# knowledge page is created for the first time), so re-running against the same
# database reports those two as failures that are state, not regressions. Point
# HINDSIGHT_API_DATABASE_URL at a fresh database (or one you have just dropped and
# recreated) and pass a binary built with -tags pgx.
set -u

BIN=${1:?usage: contract_smoke.sh <server-binary>}
PORT=${PORT:-19500}
BASE="http://127.0.0.1:${PORT}"
FAILURES=0

check() {
  local name=$1 want=$2 got=$3
  if [ "$want" = "$got" ]; then
    echo "ok   ${name}"
  else
    echo "FAIL ${name}: want ${want}, got ${got}"
    FAILURES=$((FAILURES + 1))
  fi
}

start() {
  HINDSIGHT_API_PORT=$PORT "$@" "$BIN" >/tmp/contract_smoke_server.log 2>&1 &
  SERVER_PID=$!
  for _ in $(seq 1 40); do
    if curl -fsS -o /dev/null "${BASE}/health/live" 2>/dev/null; then return 0; fi
    sleep 0.25
  done
  echo "server did not become ready; log:"
  tail -5 /tmp/contract_smoke_server.log
  exit 1
}

stop() {
  kill "$SERVER_PID" 2>/dev/null
  wait "$SERVER_PID" 2>/dev/null
}

# --- the descriptor surface the reference serves by being a FastAPI app -------
start env
check "GET /openapi.json status" 200 "$(curl -sS -o /tmp/smoke_openapi.json -w '%{http_code}' "${BASE}/openapi.json")"
check "openapi.json operations" 99 "$(python3 - <<'PY'
import json
spec = json.load(open('/tmp/smoke_openapi.json'))
print(sum(1 for item in spec['paths'].values() for method in item if method.lower() in ('get','post','put','delete','patch')))
PY
)"
check "openapi.json has no servers without a base path" False "$(grep -c '"servers"' /tmp/smoke_openapi.json | sed 's/^0$/False/;s/^[1-9].*/True/')"
check "GET /docs status" 200 "$(curl -sS -o /dev/null -w '%{http_code}' "${BASE}/docs")"
check "GET /docs/oauth2-redirect status" 200 "$(curl -sS -o /dev/null -w '%{http_code}' "${BASE}/docs/oauth2-redirect")"
check "GET /redoc status" 200 "$(curl -sS -o /dev/null -w '%{http_code}' "${BASE}/redoc")"
check "unknown path is JSON" '{"detail":"Not Found"}' "$(curl -sS "${BASE}/v1/default/banks/x/nope")"
check "wrong method is JSON" '{"detail":"Method Not Allowed"}' "$(curl -sS -X DELETE "${BASE}/health")"
check "wrong method keeps Allow" "GET, HEAD" "$(curl -sS -o /dev/null -D - -X DELETE "${BASE}/health" | awk 'tolower($1)=="allow:"{print substr($0,8)}' | tr -d '\r')"
check "version reports the reference release" 0.10.3 "$(curl -sS "${BASE}/v1/version" | python3 -c 'import json,sys; print(json.load(sys.stdin)["api_version"])')"
check "mcp flag default on" True "$(curl -sS "${BASE}/v1/version" | python3 -c 'import json,sys; print(json.load(sys.stdin)["features"]["mcp"])')"
check "bank config flag default on" True "$(curl -sS "${BASE}/v1/version" | python3 -c 'import json,sys; print(json.load(sys.stdin)["features"]["bank_config_api"])')"
check "audit log flag default off" False "$(curl -sS "${BASE}/v1/version" | python3 -c 'import json,sys; print(json.load(sys.stdin)["features"]["audit_log"])')"
stop

# --- base path serves both spellings and names itself in the document --------
start env HINDSIGHT_API_BASE_PATH=/hindsight
check "prefixed route" 200 "$(curl -sS -o /dev/null -w '%{http_code}' "${BASE}/hindsight/health/live")"
check "root route beside the prefix" 200 "$(curl -sS -o /dev/null -w '%{http_code}' "${BASE}/health/live")"
check "document names the base path" '/hindsight' "$(curl -sS "${BASE}/hindsight/openapi.json" | python3 -c 'import json,sys; print(json.load(sys.stdin)["servers"][0]["url"])')"
check "docs page reads the contract under the prefix" True "$(curl -sS "${BASE}/hindsight/docs" | grep -q "'/hindsight/openapi.json'" && echo True || echo False)"
stop

# --- tenant authentication, when configured ---------------------------------
start env HINDSIGHT_API_TENANT_EXTENSION=hindsight_api.extensions.builtin.tenant:ApiKeyTenantExtension HINDSIGHT_API_TENANT_API_KEY=s3cret
check "bank route without a key" 401 "$(curl -sS -o /tmp/smoke_auth.json -w '%{http_code}' "${BASE}/v1/default/banks")"
check "rejected body" '{"detail":"Authentication failed: Invalid API key"}' "$(cat /tmp/smoke_auth.json)"
check "bank route with the key" 200 "$(curl -sS -o /dev/null -w '%{http_code}' -H 'Authorization: Bearer s3cret' "${BASE}/v1/default/banks")"
check "monitoring stays open" 200 "$(curl -sS -o /dev/null -w '%{http_code}' "${BASE}/health")"
check "retired route answers 410 without a key" 410 "$(curl -sS -o /dev/null -w '%{http_code}' "${BASE}/v1/default/banks/x/profile")"
stop

# --- a malformed body is the framework's 422 item, not a 400 sentence -------
start env
check "malformed body status" 422 "$(curl -sS -o /tmp/smoke_body.json -w '%{http_code}' -X POST "${BASE}/v1/default/banks/b/memories" -H 'Content-Type: application/json' -d 'not-json')"
check "malformed body item" json_invalid "$(python3 -c 'import json; print(json.load(open("/tmp/smoke_body.json"))["detail"][0]["type"])')"
# A string where the model declares a list is the framework's `list_type`.
check "wrong-typed field is named" list_type "$(curl -sS -o /tmp/smoke_body2.json -w '%{http_code}' -X POST "${BASE}/v1/default/banks/b/memories" -H 'Content-Type: application/json' -d '{"items":"x"}' >/dev/null; python3 -c 'import json; print(json.load(open("/tmp/smoke_body2.json"))["detail"][0]["type"])')"
check "wrong-typed field position" 0 "$(python3 -c 'import json; loc=json.load(open("/tmp/smoke_body2.json"))["detail"][0]["loc"]; print(0 if all(isinstance(x,str) for x in loc) else 1)')"
stop

# --- module: metrics names and the profile switch ---------------------------
start env
check "requests metric name" True "$(curl -sS "${BASE}/metrics" | grep -q '^hindsight_http_requests_total' && echo True || echo False)"
check "in-progress metric name" True "$(curl -sS "${BASE}/metrics" | grep -q '^hindsight_http_requests_in_progress' && echo True || echo False)"
stop
HINDSIGHT_API_PROFILE='{"every":0}' HINDSIGHT_API_PORT=$((PORT + 100)) "$BIN" >/tmp/contract_smoke_profile.log 2>&1
check "a malformed profile request refuses startup" 1 "$?"
check "the refusal names the variable" True "$(grep -q "HINDSIGHT_API_PROFILE" /tmp/contract_smoke_profile.log && echo True || echo False)"

# --- the shapes the audit found wrong, on a bank with one memory -------------
start env
BANK="${BASE}/v1/default/banks/smoke-bank"
curl -sS -o /dev/null -X POST "${BANK}/memories" -H 'Content-Type: application/json' \
  -d '{"items":[{"content":"Alice prefers tea","tags":["a"]}]}'
check "memories/list uses fact_type" world "$(curl -sS "${BANK}/memories/list?limit=1" | python3 -c 'import json,sys; print(json.load(sys.stdin)["items"][0]["fact_type"])')"
check "memories/list items carry a list of entities" str "$(curl -sS "${BANK}/memories/list?limit=1" | python3 -c 'import json,sys; print(type(json.load(sys.stdin)["items"][0]["entities"]).__name__)')"
check "unknown consolidation_state is rejected" 400 "$(curl -sS -o /dev/null -w '%{http_code}' "${BANK}/memories/list?consolidation_state=nonsense")"
check "timeseries 1h spans 60 minute buckets" 60 "$(curl -sS "${BANK}/stats/memories-timeseries?period=1h" | python3 -c 'import json,sys; d=json.load(sys.stdin); assert d["trunc"]=="minute", d; print(len(d["buckets"]))')"
check "timeseries counts the memory" 1 "$(curl -sS "${BANK}/stats/memories-timeseries?period=1h" | python3 -c 'import json,sys; print(sum(b.get("world",0) for b in json.load(sys.stdin)["buckets"]))')"
check "an unknown period falls back to 7d" 7 "$(curl -sS "${BANK}/stats/memories-timeseries?period=nonsense" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(len(d["buckets"]))')"
check "POST /consolidate is 200" 200 "$(curl -sS -o /dev/null -w '%{http_code}' -X POST "${BANK}/consolidate" -H 'Content-Type: application/json' -d '{}')"
check "knowledge page create is 201" 201 "$(curl -sS -o /dev/null -w '%{http_code}' -X POST "${BANK}/knowledge-base/pages" -H 'Content-Type: application/json' -d '{"name":"faq","source_query":"what is tea?"}')"
check "webhook create is 201" 201 "$(curl -sS -o /tmp/smoke_webhook.json -w '%{http_code}' -X POST "${BANK}/webhooks" -H 'Content-Type: application/json' -d '{"url":"https://example.com/hook"}')"
check "webhook response keeps its null secret" None "$(python3 -c 'import json; print(json.load(open("/tmp/smoke_webhook.json"))["secret"])')"
check "webhook http_config carries defaults" 30 "$(python3 -c 'import json; print(json.load(open("/tmp/smoke_webhook.json"))["http_config"]["timeout_seconds"])')"
check "document transfer import is JSON and 200" 200 "$(curl -sS -o /dev/null -w '%{http_code}' -X POST "${BANK}/import" -H 'Content-Type: application/json' -d '{"version":"1"}')"
check "entity regenerate is 410" 410 "$(curl -sS -o /dev/null -w '%{http_code}' -X POST "${BANK}/entities/x/regenerate")"
check "malformed operation id is 400" 400 "$(curl -sS -o /dev/null -w '%{http_code}' "${BANK}/operations/not-a-uuid")"
check "unknown bank is named in the detail" "Bank 'ghost' not found" "$(curl -sS "${BASE}/v1/default/banks/ghost/stats" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("detail"))')"
stop

echo
if [ "$FAILURES" -eq 0 ]; then
  echo "all contract smoke probes passed"
else
  echo "${FAILURES} contract smoke probe(s) failed"
fi
exit "$FAILURES"