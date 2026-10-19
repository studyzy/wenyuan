#!/usr/bin/env python3
"""Fire one request at both deployments and diff the answers.

`parity_probe.py` runs a fixed list of checks; this is the same comparison for a
single request, so a route being worked on can be checked while it is being
worked on instead of by editing the probe's list:

    python3 scripts/parity_check.py GET  /v1/default/banks/parity-probe/stats
    python3 scripts/parity_check.py POST /v1/default/banks/parity-probe/memories \
        --body '{"items":[{"content":"Alice moved to Berlin."}],"async":false}'

Exit status is 0 when the status code and the normalised body agree, 1 when they
differ, 2 when a transport failed. `--reference-only` / `--service-only` print
one side, which is how a fixture is prepared (the two deployments must hold the
same fixture before a comparison means anything).

Defaults target the two deployments on this host; override with
WENYUAN_PARITY_REFERENCE[_KEY] and WENYUAN_PARITY_SERVICE[_KEY].
"""

from __future__ import annotations

import argparse
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from parity_probe import normalise, request  # noqa: E402  (same directory)

REFERENCE = os.environ.get("WENYUAN_PARITY_REFERENCE", "http://127.0.0.1:8888")
REFERENCE_KEY = os.environ.get("WENYUAN_PARITY_REFERENCE_KEY",
                               "sk-hs-f94653018553250b8ffeff305b86ee0a")
SERVICE = os.environ.get("WENYUAN_PARITY_SERVICE", "http://127.0.0.1:18100")
SERVICE_KEY = os.environ.get("WENYUAN_PARITY_SERVICE_KEY", "")


def render(value):
    return json.dumps(value, ensure_ascii=False, sort_keys=True)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("method")
    parser.add_argument("path")
    parser.add_argument("--body", default=None, help="request body as JSON text or a file path")
    parser.add_argument("--reference", default=REFERENCE)
    parser.add_argument("--reference-key", default=REFERENCE_KEY)
    parser.add_argument("--service", default=SERVICE)
    parser.add_argument("--service-key", default=SERVICE_KEY)
    parser.add_argument("--header", action="append", default=[],
                        help="extra header, name:value (repeatable)")
    parser.add_argument("--reference-only", action="store_true")
    parser.add_argument("--service-only", action="store_true")
    parser.add_argument("--show", action="store_true", help="print the bodies even when they agree")
    args = parser.parse_args()

    body = args.body
    if body and os.path.exists(body):
        with open(body, "r", encoding="utf-8") as handle:
            body = handle.read()
    headers = {}
    for item in args.header:
        name, _, value = item.partition(":")
        headers[name.strip()] = value.strip()

    method = args.method.upper()
    sides = []
    if not args.service_only:
        sides.append(("reference", args.reference, args.reference_key))
    if not args.reference_only:
        sides.append(("service", args.service, args.service_key))

    answers = {}
    failed = False
    for name, base, key in sides:
        status, payload = request(base, method, args.path, body, key, headers=headers or None)
        answers[name] = (status, payload)
        if status == 0:
            failed = True
        print(f"{name:9} [{status}] {render(payload)[:4000]}")
        if len(render(payload)) > 4000:
            print(f"{name:9} … body truncated ({(len(render(payload)))} chars)")

    if failed or len(answers) < 2:
        return 2 if failed else 0

    want = (answers["reference"][0], normalise(answers["reference"][1]))
    got = (answers["service"][0], normalise(answers["service"][1]))
    if want == got:
        print("\nSAME")
        return 0
    print("\nDIFF")
    print(f"  reference: {render(want[1])[:2000]}")
    print(f"  service:   {render(got[1])[:2000]}")
    return 1


if __name__ == "__main__":
    sys.exit(main())