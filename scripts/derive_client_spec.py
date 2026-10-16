"""Derive the client-generation OpenAPI document from the served one.

The document this service serves at `/openapi.json` is the reference's contract
and keeps the reference's own branding: a client that fetches it must see the
same document a reference deployment serves. The SDKs under `clients/` are this
project's own artifacts, so the generator reads a derived copy in which the
branding is replaced — every occurrence of the reference's name becomes this
project's name with the original's case (`Hindsight` -> `WenYuan`, `HINDSIGHT`
-> `WENYUAN`, `hindsight` -> `wenyuan`). Object keys are rewritten too, so a
`$ref` or a schema name cannot smuggle the old branding through.

One carve-out: `HINDSIGHT_API_*` environment-variable names are the deployment
contract this service reads, so they are preserved verbatim. A client document
that renamed them would name keys no deployment answers.

    python3 scripts/derive_client_spec.py            # write clients/openapi.json
    python3 scripts/derive_client_spec.py --check    # fail when the copy is stale
"""

import argparse
import json
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SOURCE = os.path.join(ROOT, "internal", "httpx", "openapi.json")
TARGET = os.path.join(ROOT, "clients", "openapi.json")

# Environment names the deployment answers; never rewritten.
ENV_NAME = re.compile(r"HINDSIGHT_API_[A-Z0-9_]*")
BRAND = re.compile(r"(?i)hindsight")


def brand(text: str) -> str:
    """Rewrite one string's branding, keeping the original's case."""
    placeholders: list[str] = []

    def protect(match: re.Match) -> str:
        placeholders.append(match.group(0))
        return "\x00%d\x00" % (len(placeholders) - 1)

    def renamed(match: re.Match) -> str:
        word = match.group(0)
        if word.isupper():
            return "WENYUAN"
        if word[0].isupper():
            return "WenYuan"
        return "wenyuan"

    protected = ENV_NAME.sub(protect, text)
    protected = BRAND.sub(renamed, protected)
    for index, name in enumerate(placeholders):
        protected = protected.replace("\x00%d\x00" % index, name)
    return protected


def rewrite(node):
    """Walk a JSON document, rewriting every string and object key."""
    if isinstance(node, str):
        return brand(node)
    if isinstance(node, list):
        return [rewrite(item) for item in node]
    if isinstance(node, dict):
        return {brand(key): rewrite(value) for key, value in node.items()}
    return node


def render(document: dict) -> str:
    return json.dumps(document, ensure_ascii=False, indent=2) + "\n"


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", default=SOURCE,
                        help="the served OpenAPI document (default: the embedded copy)")
    parser.add_argument("--output", default=TARGET,
                        help="where to write the derived document")
    parser.add_argument("--check", action="store_true",
                        help="fail when the committed copy differs from the derivation")
    args = parser.parse_args()

    with open(args.source, "r", encoding="utf-8") as handle:
        served = json.load(handle)
    derived = render(rewrite(served))

    if args.check:
        try:
            with open(args.output, "r", encoding="utf-8") as handle:
                current = handle.read()
        except FileNotFoundError:
            print("the derived document is missing: %s" % args.output, file=sys.stderr)
            return 1
        if current != derived:
            print("the derived document is stale; regenerate with "
                  "scripts/derive_client_spec.py", file=sys.stderr)
            return 1
        print("the derived document matches %s" % os.path.relpath(args.source, ROOT))
        return 0

    os.makedirs(os.path.dirname(args.output), exist_ok=True)
    with open(args.output, "w", encoding="utf-8") as handle:
        handle.write(derived)
    print("wrote %s" % os.path.relpath(args.output, ROOT))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())