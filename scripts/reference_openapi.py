"""Capture the reference's OpenAPI document.

The HTTP contract this project serves is the reference's contract, so the
document served at `/openapi.json` is not hand-written: it is captured from the
reference service itself, exactly as the migration series is captured from the
reference database. That is what makes a client generated from the served
document generate against the surface a reference deployment exposes.

The reference is the running service or the document it publishes:

    python3 scripts/reference_openapi.py                      # the default service URL
    python3 scripts/reference_openapi.py --source http://api.internal:8000/openapi.json
    python3 scripts/reference_openapi.py --source /path/to/static/openapi.json
    # override with WENYUAN_REFERENCE_OPENAPI_URL

Output: internal/httpx/openapi.json (the embedded copy the binary serves).

Only the document is captured. The `servers` entry is not: the reference adds it
per response from its configured root path, and so does this service, from
WENYUAN_API_BASE_PATH.
"""

import argparse
import json
import os
import urllib.request

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
TARGET = os.path.join(ROOT, "internal", "httpx", "openapi.json")

SOURCE = os.environ.get("WENYUAN_REFERENCE_OPENAPI_URL",
                        "http://127.0.0.1:8000/openapi.json")


def load(source: str) -> dict:
    if source.startswith("http://") or source.startswith("https://"):
        with urllib.request.urlopen(source, timeout=30) as response:
            return json.loads(response.read().decode())
    with open(source, "r", encoding="utf-8") as handle:
        return json.load(handle)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", default=SOURCE,
                        help="reference service URL or a local document path")
    parser.add_argument("--check", action="store_true",
                        help="fail when the committed copy differs from the reference")
    args = parser.parse_args()

    document = load(args.source)
    # The reference injects `servers` per response when a root path is
    # configured, so the captured document never carries one; strip it if a
    # published copy happens to include it.
    document.pop("servers", None)
    # The reference's response carries no trailing newline, so neither does the
    # captured copy: /openapi.json is byte-identical to the live document.
    rendered = json.dumps(document, ensure_ascii=False, separators=(",", ":"))

    if args.check:
        with open(TARGET, "r", encoding="utf-8") as handle:
            if handle.read() != rendered:
                print("internal/httpx/openapi.json differs from %s; regenerate it" % args.source)
                return 1
        print("internal/httpx/openapi.json matches %s" % args.source)
        return 0

    os.makedirs(os.path.dirname(TARGET), exist_ok=True)
    with open(TARGET, "w", encoding="utf-8") as handle:
        handle.write(rendered)
    operations = sum(1 for item in document.get("paths", {}).values() for method in item
                     if method.lower() in ("get", "post", "put", "delete", "patch"))
    print("wrote %s: %d operations, %d schemas, version %s" % (
        os.path.relpath(TARGET, ROOT),
        operations,
        len(document.get("components", {}).get("schemas", {})),
        document.get("info", {}).get("version"),
    ))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())