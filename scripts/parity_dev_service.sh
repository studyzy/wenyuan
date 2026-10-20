#!/usr/bin/env bash
# Run this working tree as a service for one parity comparison.
#
# A route's behavior is compared against the live reference deployment by
# sending the same request to both and diffing the answers
# (`scripts/parity_check.py`). That needs this tree served somewhere, rebuilt
# after every edit, without disturbing the long-running deployment or another
# comparison running at the same time — so each caller gets its own port:
#
#   scripts/parity_dev_service.sh 18201            # build, start, print the pid
#   scripts/parity_check.py GET /v1/default/banks \
#       --service http://127.0.0.1:18201
#   scripts/parity_dev_service.sh 18201 --stop     # stop it
#
# The database is the shared development one, so a comparison sees migrated
# data; the embedded worker and maintenance loop are switched off so a sandbox
# cannot consume the queue or start maintenance that belongs to the real
# deployment. Fixtures created for a comparison must be deleted by the caller.
#
# The worker is off by default because it drains the shared queue, but a route
# that answers from what the worker produces (a mental model's content after a
# refresh, any row history, a memory's last_seen) reads as a difference against
# a reference whose worker is running. Set PARITY_DEV_WORKER_ENABLED=true for
# those comparisons — on a port of its own, since the worker consumes the queue.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
PORT=${1:?usage: parity_dev_service.sh <port> [--stop]}
STOP=${2:-}
LOG=${PARITY_DEV_LOG:-/tmp/wenyuan-parity-$PORT.log}
BIN=${PARITY_DEV_BIN:-/tmp/wenyuan-parity-$PORT}

if [ "$STOP" = "--stop" ]; then
  if [ -f "$BIN.pid" ]; then
    kill "$(cat "$BIN.pid")" 2>/dev/null || true
    rm -f "$BIN.pid"
  fi
  exit 0
fi

if [ -f "$BIN.pid" ]; then
  kill "$(cat "$BIN.pid")" 2>/dev/null || true
  sleep 0.5
fi

(cd "$ROOT" && CGO_ENABLED=0 go build -tags pgx -o "$BIN" ./cmd/server)

# The reference's own keys are the contract, so the deployment-shaped settings
# come from etc/wenyuan.yaml exactly as the long-running service reads them.
cd "$ROOT"
HINDSIGHT_API_PORT="$PORT" \
HINDSIGHT_API_WORKER_ENABLED="${PARITY_DEV_WORKER_ENABLED:-false}" \
HINDSIGHT_API_DATABASE_URL="${PARITY_DEV_DATABASE_URL:-postgresql://wenyuan:wenyuan@127.0.0.1:15432/wenyuan}" \
  "$BIN" >"$LOG" 2>&1 &
echo $! >"$BIN.pid"

for _ in $(seq 1 60); do
  if curl -sf -m 2 "http://127.0.0.1:$PORT/health/live" >/dev/null; then
    echo "serving on http://127.0.0.1:$PORT (pid $(cat "$BIN.pid"), log $LOG)"
    exit 0
  fi
  sleep 0.5
done

echo "the service did not come up; see $LOG" >&2
tail -20 "$LOG" >&2
exit 1