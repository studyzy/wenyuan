#!/usr/bin/env python3
"""Differentially probe this service against a live reference deployment.

Run a fixed list of requests against two services and compare the status code and
the parsed body of each. The point is the checks a black-box story suite does not
make: validation envelopes, not-found precedence, parameter bounds, response
shapes on empty banks, gate wording, descriptor routes, MCP transport errors.

    python3 scripts/parity_probe.py \
        --reference http://127.0.0.1:8888 --reference-key sk-hs-... \
        --service http://127.0.0.1:18000

Everything is read-only except the fixture it creates and deletes: one bank, a
second one for the clone/transfer targets, and the objects it posts to them.
Each run names its fixture banks after its own pid (or `--run-id`), so two probes
against the same deployment cannot see each other's fixtures — without that, one
run's `reflect` creates the bank a concurrent run's `recall` then finds, and both
report a difference that is only the other run's state.
Timestamps and generated ids are normalised away, so a difference reported here
is a difference in shape or wording, not in clock time.

Exit status is 1 when a check differs, so it can gate a change.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
import urllib.error
import urllib.request

# An instant, with or without a time part: a prompt renders the current date in
# prose ("Today is 2026-10-17"), so a bare date must be blanked too.
TIMESTAMP = re.compile(
    r"\d{4}-\d{2}-\d{2}([T ]\d{2}:\d{2}(:\d{2})?(\.\d+)?([+-]\d{2}:?\d{2}|Z)?)?"
)
UUID = re.compile(
    r"\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b"
)
HEX32 = re.compile(r"\b[0-9a-fA-F]{32}\b")
MM_ID = re.compile(r"\bmm-[0-9a-f]{16,}\b")
KP_ID = re.compile(r"\bk[pf]-[0-9a-f]{16,}\b")


# Fields whose value is a measurement, not a contract: two services cannot report
# the same milliseconds or uptime.
MEASUREMENTS = {"db_acquire_ms", "uptime_seconds", "seconds_since_last_poll"}


def normalise(value):
    """Blank out the parts of a payload that legitimately differ per run."""
    if isinstance(value, str):
        value = TIMESTAMP.sub("<TS>", value)
        value = UUID.sub("<UUID>", value)
        value = HEX32.sub("<HEX32>", value)
        value = MM_ID.sub("<MM>", value)
        value = KP_ID.sub("<KP>", value)
        return value
    if isinstance(value, list):
        return [normalise(item) for item in value]
    if isinstance(value, dict):
        return {
            key: "<N>" if is_measurement(key) else normalise(item)
            for key, item in value.items()
        }
    return value


def is_measurement(key):
    """A model's token counters depend on the model each deployment runs, so they
    are a measurement of that model rather than a value two services must agree
    on."""
    return key in MEASUREMENTS or key.endswith("_tokens")


# Feature flags a deployment sets for itself rather than something a client reads
# as a contract: two services are expected to differ when one of them runs
# without an embedded worker (`scripts/parity_dev_service.sh` switches it off so a
# sandbox cannot consume the real deployment's queue). The version response is
# compared with these members blanked; their presence is still checked.
DEPLOYMENT_SETTINGS = {"features.worker"}


def without_deployment_settings(value):
    """Blank the feature flags that describe a deployment's own configuration."""
    if not isinstance(value, dict) or not isinstance(value.get("features"), dict):
        return value
    value = dict(value)
    value["features"] = {
        key: ("<SETTING>" if f"features.{key}" in DEPLOYMENT_SETTINGS else item)
        for key, item in value["features"].items()
    }
    return value


def request(base, method, path, body=None, key=None, content_type="application/json", headers=None):
    """Issue one request, returning (status, parsed body or text)."""
    status, payload, _, _ = request_full(base, method, path, body, key, content_type, headers)
    return status, payload


def request_full(base, method, path, body=None, key=None, content_type="application/json", headers=None):
    """Issue one request, returning (status, parsed body or text, response
    headers lower-cased by name, and the response body length in bytes). The
    header and length results let a check compare framing instead of a body."""
    data = body.encode() if isinstance(body, str) else None
    req = urllib.request.Request(base + path, data=data, method=method)
    if key:
        req.add_header("Authorization", "Bearer " + key)
    for name, value in (headers or {}).items():
        req.add_header(name, value)
    if data is not None:
        req.add_header("Content-Type", content_type)
    req.add_header("Accept", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=30) as response:
            payload = response.read()
            status = response.status
            raw_headers = response.headers
    except urllib.error.HTTPError as error:
        payload = error.read()
        status = error.code
        raw_headers = error.headers
    except Exception as error:  # noqa: BLE001 - the status is the datum
        return 0, f"<transport error: {error}>", {}, 0
    response_headers = {name.lower(): value for name, value in (raw_headers or {}).items()}
    try:
        parsed = json.loads(payload.decode("utf-8", "replace"))
    except Exception:  # noqa: BLE001 - a non-JSON body is compared as text
        parsed = payload.decode("utf-8", "replace")
    return status, parsed, response_headers, len(payload)


def header_view(response_headers, body_length, names):
    """Reduce a response to the headers a check names, for a comparison across
    deployments. Content-Length is compared as its self-consistency (does the
    header equal the body length?) rather than as one number, because the two
    deployments' bodies can differ in length while both must still frame the
    body; every other named header is compared by value, with an absent header
    rendering as None."""
    view = {}
    for name in names:
        if name == "content-length":
            view[name] = response_headers.get(name) == str(body_length)
        else:
            view[name] = response_headers.get(name)
    return view


# Count-bearing envelopes whose values legitimately differ between two services
# with different histories: compare their status and their shape (the key sets),
# not the numbers.
SHAPE_ONLY = {
    "stats",
    "stats refresh",
    "llm requests",
    "llm stats",
    "operations list",
    "operations ghost bank",
    "stats ghost",
    "banks list limit 0",
    "banks list q",
    "banks list offset bad",
}


def shape_of(value, depth=0):
    """The key set of a payload, ignoring the values and their kinds (a count
    envelope legitimately carries different numbers on two services)."""
    if isinstance(value, dict):
        return sorted(value.keys()) + [v for item in value.values() for v in _nested_keys(item, depth)]
    return []


def _nested_keys(value, depth):
    """The key sets of the first element of every nested list, so a paginated
    envelope is compared on the item shape too."""
    if depth > 3:
        return []
    if isinstance(value, list) and value:
        return [json.dumps(shape_of(value[0], depth + 1), sort_keys=True)]
    if isinstance(value, dict):
        return [part for item in value.values() for part in _nested_keys(item, depth + 1)]
    return []


# Differences that are documented elsewhere rather than fixable here, printed
# under their own heading and not counted as failures.
KNOWN_DIFFERENCES = {
    "health": "the pool utilization counts are live measurements (each process reports its own idle/in-use connections), so they cannot agree across two processes; db_pool_waiting now matches",
    "health ready": "same as /health",
    "directive get missing": "driver error text only (asyncpg vs pgx); the status matches",
    "directive patch missing": "driver error text only (asyncpg vs pgx); the status matches",
    "directive delete missing": "driver error text only (asyncpg vs pgx); the status matches",
    "knowledge tree bad groups": "the reference embeds pydantic's multi-line error text in the detail string",
    "config patch bad key": "the reference samples sorted(list(a_set)[:10]); a Python str set's iteration order follows hash randomization, so the ten names change per process and are not the alphabetically first ten even when PYTHONHASHSEED is pinned. The field COUNT (51) and the rejection wording do match; listing ten alphabetically-first fields is the deterministic rendering of the same fact",
    "config patch bad defense": "same as the unknown-key case",
    "dry run ghost bank": "the fact text is the chat model's output, and each deployment runs a different model",
    "reflect ghost bank": "the answer is the chat model's output, and each deployment runs a different model",
    "prompts preview reflect": "the prompt carries the bank's resolved configuration, and a config write reaches the two deployments differently because the live one runs an older build than this checkout",
}


def bank_list_shape(value):
    """Reduce a bank list to the envelope plus the key set of each entry, because
    the two deployments hold different banks."""
    if not isinstance(value, dict):
        return value
    # `total` is the number of banks the deployment holds, which differs by design.
    shape = {key: item for key, item in value.items() if key not in {"banks", "total"}}
    entries = value.get("banks")
    if isinstance(entries, list):
        shape["banks"] = sorted(
            {key for entry in entries if isinstance(entry, dict) for key in entry}
        )
    return shape


def checks(bank: str, other: str, ghost: str, clone_target: str):
    """The request list. Every entry is (name, method, path, body-or-None), with
    an optional request-headers dict and an optional list of response-header
    names. An entry that names response headers is compared on its status and
    those headers instead of on its body."""
    return [
        # Descriptor and monitoring surfaces.
        ("version", "GET", "/version", None),
        ("version v1", "GET", "/v1/version", None),
        ("health", "GET", "/health", None),
        ("health ready", "GET", "/health/ready", None),
        ("health live", "GET", "/health/live", None),
        ("bank template schema", "GET", "/v1/bank-template-schema", None),
        ("unknown route", "GET", "/v1/default/nope", None),
        ("unknown route ignored params", "GET", "/v1/default/nope", None, None, ["x-ignored-params"]),
        ("wrong method", "POST", "/version", None),
        ("wrong method ignored params", "POST", "/version", None, None, ["x-ignored-params"]),
        ("version framing", "GET", "/version", None, None, ["content-length", "vary"]),
        ("openapi framing", "GET", "/openapi.json", None, None, ["content-length", "vary"]),
        # Bank collection and lifecycle.
        ("banks list", "GET", "/v1/default/banks?limit=1", None),
        ("banks list q", "GET", "/v1/default/banks?q=zzz-none", None),
        ("banks list limit 0", "GET", "/v1/default/banks?limit=0", None),
        ("banks list limit bad", "GET", "/v1/default/banks?limit=abc", None),
        ("banks list offset bad", "GET", "/v1/default/banks?offset=-1", None),
        ("bank put", "PUT", f"/v1/default/banks/{bank}", json.dumps({"name": "probe"})),
        ("bank put bad id", "PUT", "/v1/default/banks/" + ("x" * 200), json.dumps({"name": "p"})),
        ("bank put disposition partial", "PUT", f"/v1/default/banks/{other}", json.dumps({"name": "o", "disposition": {"skepticism": 5}})),
        ("bank patch name", "PATCH", f"/v1/default/banks/{bank}", json.dumps({"name": "probe2"})),
        ("bank patch empty name", "PATCH", f"/v1/default/banks/{bank}", json.dumps({"name": ""})),
        ("bank patch bad trait", "PATCH", f"/v1/default/banks/{bank}", json.dumps({"disposition_skepticism": 99})),
        ("bank delete unknown", "DELETE", f"/v1/default/banks/{ghost}", None),
        # Aliases.
        ("aliases list", "GET", f"/v1/default/banks/{bank}/aliases", None),
        ("aliases create", "POST", f"/v1/default/banks/{bank}/aliases", json.dumps({"alias": "probe-alias"})),
        ("aliases create dup", "POST", f"/v1/default/banks/{bank}/aliases", json.dumps({"alias": "probe-alias"})),
        ("aliases create no body", "POST", f"/v1/default/banks/{bank}/aliases", "{}"),
        ("aliases list after", "GET", f"/v1/default/banks/{bank}/aliases", None),
        ("alias promote missing", "PATCH", f"/v1/default/banks/{bank}/aliases/nope", json.dumps({"primary": True})),
        ("alias remove missing", "DELETE", f"/v1/default/banks/{bank}/aliases/nope", None),
        # Configuration.
        ("config get", "GET", f"/v1/default/banks/{bank}/config", None),
        ("config patch empty", "PATCH", f"/v1/default/banks/{bank}/config", json.dumps({"updates": {}})),
        ("config patch bad key", "PATCH", f"/v1/default/banks/{bank}/config", json.dumps({"updates": {"nope": 1}})),
        ("config patch bad type", "PATCH", f"/v1/default/banks/{bank}/config", json.dumps({"updates": {"retain_chunk_size": "x"}})),
        ("config patch bad defense", "PATCH", f"/v1/default/banks/{bank}/config", json.dumps({"updates": {"memory_defense": "nope"}})),
        ("config patch null updates", "PATCH", f"/v1/default/banks/{bank}/config", json.dumps({"updates": None})),
        ("config delete", "DELETE", f"/v1/default/banks/{bank}/config", None),
        # Memory collection and validation.
        ("memories list", "GET", f"/v1/default/banks/{bank}/memories/list", None),
        ("memories list repeated limit", "GET", f"/v1/default/banks/{bank}/memories/list?limit=1&limit=50", None),
        ("memories list limit 0", "GET", f"/v1/default/banks/{bank}/memories/list?limit=0", None),
        ("memories list tags_match bogus", "GET", f"/v1/default/banks/{bank}/memories/list?tags_match=bogus", None),
        ("memories list consolidation bogus", "GET", f"/v1/default/banks/{bank}/memories/list?consolidation_state=bogus", None),
        ("memories list state empty", "GET", f"/v1/default/banks/{bank}/memories/list?state=", None),
        ("memories list time_field bogus", "GET", f"/v1/default/banks/{bank}/memories/list?time_field=bogus", None),
        ("memories list inverted window", "GET", f"/v1/default/banks/{bank}/memories/list?start_date=2025-01-02&end_date=2025-01-01", None),
        ("memories list bad date", "GET", f"/v1/default/banks/{bank}/memories/list?start_date=notadate", None),
        ("memory get missing", "GET", f"/v1/default/banks/{bank}/memories/00000000-0000-0000-0000-000000000000", None),
        ("memory get bad id", "GET", f"/v1/default/banks/{bank}/memories/not-a-uuid", None),
        ("memory patch missing", "PATCH", f"/v1/default/banks/{bank}/memories/00000000-0000-0000-0000-000000000000", json.dumps({"text": "x"})),
        ("memory patch empty body", "PATCH", f"/v1/default/banks/{bank}/memories/00000000-0000-0000-0000-000000000000", "{}"),
        ("memory history missing", "GET", f"/v1/default/banks/{bank}/memories/00000000-0000-0000-0000-000000000000/history", None),
        ("observations delete unknown bank", "DELETE", f"/v1/default/banks/{ghost}/observations", None),
        ("memory observations delete unknown bank", "DELETE", f"/v1/default/banks/{ghost}/memories/00000000-0000-0000-0000-000000000000/observations", None),
        ("observation scopes", "GET", f"/v1/default/banks/{bank}/observations/scopes", None),
        ("observation scopes limit 0", "GET", f"/v1/default/banks/{bank}/observations/scopes?limit=0", None),
        ("observation scopes limit 1001", "GET", f"/v1/default/banks/{bank}/observations/scopes?limit=1001", None),
        # Recall / reflect / retain request validation.
        ("recall empty body", "POST", f"/v1/default/banks/{bank}/memories/recall", "{}"),
        ("recall bad budget", "POST", f"/v1/default/banks/{bank}/memories/recall", json.dumps({"query": "q", "budget": "nope"})),
        ("recall bad tags_match", "POST", f"/v1/default/banks/{bank}/memories/recall", json.dumps({"query": "q", "tags_match": "nope"})),
        ("recall bad include", "POST", f"/v1/default/banks/{bank}/memories/recall", json.dumps({"query": "q", "include": {"entities": True}})),
        ("reflect empty body", "POST", f"/v1/default/banks/{bank}/reflect", "{}"),
        ("reflect bad include", "POST", f"/v1/default/banks/{bank}/reflect", json.dumps({"query": "q", "include": None})),
        ("retain empty items", "POST", f"/v1/default/banks/{bank}/memories", json.dumps({"items": []})),
        ("retain nested bad metadata", "POST", f"/v1/default/banks/{bank}/memories", json.dumps({"items": [{"content": "hi", "metadata": 5}]})),
        ("retain item not object", "POST", f"/v1/default/banks/{bank}/memories", json.dumps({"items": [1]})),
        # A valid lax body must be accepted (it used to panic). It writes a real
        # document, so it targets the `other` bank and leaves the main probe
        # bank's document list empty for the document checks that follow.
        ("retain lax async word", "POST", f"/v1/default/banks/{other}/memories", json.dumps({"items": [{"content": "hi"}], "async": "yes"})),
        ("retain bad operation_id", "POST", f"/v1/default/banks/{bank}/memories", json.dumps({"items": [{"content": "x"}], "async": True, "operation_id": "nope"})),
        ("retain unknown field", "POST", f"/v1/default/banks/{bank}/memories", json.dumps({"items": [{"content": "x"}], "background": "b"})),
        ("retain clear unknown bank", "DELETE", f"/v1/default/banks/{ghost}/memories", None),
        ("file retain no multipart", "POST", f"/v1/default/banks/{bank}/files/retain", json.dumps({})),
        ("dry run ghost bank", "POST", f"/v1/default/banks/{ghost}/memories/dry-run-extract", json.dumps({"content": "Alice moved to Berlin."})),
        ("dry run empty content", "POST", f"/v1/default/banks/{bank}/memories/dry-run-extract", json.dumps({"content": ""})),
        ("dry run missing content", "POST", f"/v1/default/banks/{bank}/memories/dry-run-extract", "{}"),
        ("dry run bad block", "POST", f"/v1/default/banks/{bank}/memories/dry-run-extract", json.dumps({"content": [{"type": "nope"}]})),
        ("recall ghost bank", "POST", f"/v1/default/banks/{ghost}/memories/recall", json.dumps({"query": "q"})),
        ("reflect ghost bank", "POST", f"/v1/default/banks/{ghost}/reflect", json.dumps({"query": "q"})),
        ("consolidate ghost bank", "POST", f"/v1/default/banks/{ghost}/consolidate", None),
        ("consolidation recover", "POST", f"/v1/default/banks/{bank}/consolidation/recover", None),
        ("consolidation preview empty", "POST", f"/v1/default/banks/{bank}/consolidation-strategies/preview", "{}"),
        ("consolidation preview bad limit", "POST", f"/v1/default/banks/{bank}/consolidation-strategies/preview", json.dumps({"strategies": [{"name": "s", "sample_limit": 99}]})),
        ("prompts preview retain", "POST", f"/v1/default/banks/{bank}/prompts/preview", json.dumps({"operation": "retain"})),
        ("prompts preview consolidation", "POST", f"/v1/default/banks/{bank}/prompts/preview", json.dumps({"operation": "consolidation"})),
        ("prompts preview reflect", "POST", f"/v1/default/banks/{bank}/prompts/preview", json.dumps({"operation": "reflect"})),
        ("prompts preview bad op", "POST", f"/v1/default/banks/{bank}/prompts/preview", json.dumps({"operation": "nope"})),
        ("prompts preview ghost bank", "POST", f"/v1/default/banks/{ghost}/prompts/preview", json.dumps({"operation": "retain"})),
        # Documents, chunks, tags.
        ("documents list", "GET", f"/v1/default/banks/{bank}/documents", None),
        ("documents list repeated limit", "GET", f"/v1/default/banks/{bank}/documents?limit=1&limit=50", None),
        ("documents list limit 0", "GET", f"/v1/default/banks/{bank}/documents?limit=0", None),
        ("documents list time_field bogus", "GET", f"/v1/default/banks/{bank}/documents?time_field=bogus", None),
        ("documents list inverted window", "GET", f"/v1/default/banks/{bank}/documents?start_date=2025-01-02&end_date=2025-01-01", None),
        ("document get missing", "GET", f"/v1/default/banks/{bank}/documents/nope", None),
        ("document chunks missing", "GET", f"/v1/default/banks/{bank}/documents/nope/chunks", None),
        ("document reprocess missing", "POST", f"/v1/default/banks/{bank}/documents/nope/reprocess", None),
        ("document patch empty", "PATCH", f"/v1/default/banks/{bank}/documents/nope", "{}"),
        ("document delete missing", "DELETE", f"/v1/default/banks/{bank}/documents/nope", None),
        ("document get ghost bank", "GET", f"/v1/default/banks/{ghost}/documents/nope", None),
        ("tags", "GET", f"/v1/default/banks/{bank}/tags", None),
        ("tags ghost bank", "GET", f"/v1/default/banks/{ghost}/tags", None),
        ("tags source bogus", "GET", f"/v1/default/banks/{bank}/tags?source=bogus", None),
        ("tags limit 0", "GET", f"/v1/default/banks/{bank}/tags?limit=0", None),
        ("chunk missing", "GET", "/v1/default/chunks/nope", None),
        ("chunk unclean path", "GET", "/v1/default/chunks//nope", None),
        # Entities and graph.
        ("entities list", "GET", f"/v1/default/banks/{bank}/entities", None),
        ("entities list tags_match bogus", "GET", f"/v1/default/banks/{bank}/entities?tags_match=bogus", None),
        ("entities list limit 0", "GET", f"/v1/default/banks/{bank}/entities?limit=0", None),
        ("entity graph", "GET", f"/v1/default/banks/{bank}/entities/graph", None),
        ("entity get missing", "GET", f"/v1/default/banks/{bank}/entities/00000000-0000-0000-0000-000000000000", None),
        ("entity get bad id", "GET", f"/v1/default/banks/{bank}/entities/notauuid", None),
        ("entity regenerate", "POST", f"/v1/default/banks/{bank}/entities/00000000-0000-0000-0000-000000000000/regenerate", None),
        ("graph", "GET", f"/v1/default/banks/{bank}/graph", None),
        ("graph repeated limit", "GET", f"/v1/default/banks/{bank}/graph?limit=1&limit=50", None),
        ("graph limit 0", "GET", f"/v1/default/banks/{bank}/graph?limit=0", None),
        ("graph tags_match bogus", "GET", f"/v1/default/banks/{bank}/graph?tags_match=bogus", None),
        # Mental models.
        ("mental models list", "GET", f"/v1/default/banks/{bank}/mental-models", None),
        ("mental models tags_match bogus", "GET", f"/v1/default/banks/{bank}/mental-models?tags_match=bogus", None),
        ("mental models detail bogus", "GET", f"/v1/default/banks/{bank}/mental-models?detail=bogus", None),
        ("mental models limit 0", "GET", f"/v1/default/banks/{bank}/mental-models?limit=0", None),
        ("mental model get missing", "GET", f"/v1/default/banks/{bank}/mental-models/nope", None),
        ("mental model history missing", "GET", f"/v1/default/banks/{bank}/mental-models/nope/history", None),
        ("mental model create bad", "POST", f"/v1/default/banks/{bank}/mental-models", json.dumps({"name": "m", "max_tokens": 10})),
        ("mental model create no source", "POST", f"/v1/default/banks/{bank}/mental-models", json.dumps({"name": "m"})),
        ("mental model patch missing", "PATCH", f"/v1/default/banks/{bank}/mental-models/nope", json.dumps({"name": "x"})),
        ("mental model delete missing", "DELETE", f"/v1/default/banks/{bank}/mental-models/nope", None),
        ("mental model refresh missing", "POST", f"/v1/default/banks/{bank}/mental-models/nope/refresh", None),
        ("mental model dry run missing", "POST", f"/v1/default/banks/{bank}/mental-models/nope/dry-run-refresh", None),
        ("mental model clear missing", "POST", f"/v1/default/banks/{bank}/mental-models/nope/clear", None),
        # Directives.
        ("directives list", "GET", f"/v1/default/banks/{bank}/directives", None),
        ("directive get missing", "GET", f"/v1/default/banks/{bank}/directives/nope", None),
        ("directive create no body", "POST", f"/v1/default/banks/{bank}/directives", "{}"),
        ("directive create", "POST", f"/v1/default/banks/{bank}/directives", json.dumps({"name": "d", "content": "c"})),
        ("directive patch missing", "PATCH", f"/v1/default/banks/{bank}/directives/nope", json.dumps({"name": "x"})),
        ("directive delete missing", "DELETE", f"/v1/default/banks/{bank}/directives/nope", None),
        # Knowledge base.
        ("knowledge tree", "GET", f"/v1/default/banks/{bank}/knowledge-base/tree", None),
        ("knowledge tree bad groups", "GET", f"/v1/default/banks/{bank}/knowledge-base/tree?tag_groups=notjson", None),
        ("knowledge search no q", "GET", f"/v1/default/banks/{bank}/knowledge-base/search", None),
        ("knowledge search limit 0", "GET", f"/v1/default/banks/{bank}/knowledge-base/search?q=a&limit=0", None),
        ("knowledge search limit 51", "GET", f"/v1/default/banks/{bank}/knowledge-base/search?q=a&limit=51", None),
        ("knowledge search empty limit", "GET", f"/v1/default/banks/{bank}/knowledge-base/search?q=a&limit=", None),
        ("knowledge export", "GET", f"/v1/default/banks/{bank}/knowledge-base/export", None),
        ("knowledge page missing", "GET", f"/v1/default/banks/{bank}/knowledge-base/pages/nope", None),
        ("knowledge node patch missing", "PATCH", f"/v1/default/banks/{bank}/knowledge-base/nodes/nope", json.dumps({"name": "x"})),
        ("knowledge node delete missing", "DELETE", f"/v1/default/banks/{bank}/knowledge-base/nodes/nope", None),
        ("knowledge folder empty", "POST", f"/v1/default/banks/{bank}/knowledge-base/folders", "{}"),
        ("knowledge page no source", "POST", f"/v1/default/banks/{bank}/knowledge-base/pages", json.dumps({"name": "p"})),
        # Observability.
        ("stats", "GET", f"/v1/default/banks/{bank}/stats", None),
        ("stats refresh", "GET", f"/v1/default/banks/{bank}/stats?refresh=true", None),
        ("stats ghost", "GET", f"/v1/default/banks/{ghost}/stats", None),
        ("timeseries", "GET", f"/v1/default/banks/{bank}/stats/memories-timeseries", None),
        ("timeseries bad period", "GET", f"/v1/default/banks/{bank}/stats/memories-timeseries?period=nope", None),
        ("timeseries bad field", "GET", f"/v1/default/banks/{bank}/stats/memories-timeseries?time_field=nope", None),
        ("health llm", "POST", f"/v1/default/banks/{bank}/health/llm", None),
        ("audit logs", "GET", f"/v1/default/banks/{bank}/audit-logs", None),
        ("audit logs limit 0", "GET", f"/v1/default/banks/{bank}/audit-logs?limit=0", None),
        ("audit logs limit 501", "GET", f"/v1/default/banks/{bank}/audit-logs?limit=501", None),
        ("audit logs bad date", "GET", f"/v1/default/banks/{bank}/audit-logs?start_date=notadate", None),
        ("audit stats", "GET", f"/v1/default/banks/{bank}/audit-logs/stats", None),
        ("audit stats bad period", "GET", f"/v1/default/banks/{bank}/audit-logs/stats?period=nope", None),
        ("llm requests", "GET", f"/v1/default/banks/{bank}/llm-requests", None),
        ("llm requests limit 501", "GET", f"/v1/default/banks/{bank}/llm-requests?limit=501", None),
        ("llm requests bad group", "GET", f"/v1/default/banks/{bank}/llm-requests?group=maybe", None),
        ("llm stats", "GET", f"/v1/default/banks/{bank}/llm-requests/stats", None),
        # Operations.
        ("operations list", "GET", f"/v1/default/banks/{bank}/operations", None),
        ("operations limit 0", "GET", f"/v1/default/banks/{bank}/operations?limit=0", None),
        ("operations limit 101", "GET", f"/v1/default/banks/{bank}/operations?limit=101", None),
        ("operations exclude bad", "GET", f"/v1/default/banks/{bank}/operations?exclude_parents=maybe", None),
        ("operations ghost bank", "GET", f"/v1/default/banks/{ghost}/operations", None),
        ("operation get missing", "GET", f"/v1/default/banks/{bank}/operations/00000000-0000-0000-0000-000000000000", None),
        ("operation get bad id", "GET", f"/v1/default/banks/{bank}/operations/nope", None),
        ("operation cancel missing", "DELETE", f"/v1/default/banks/{bank}/operations/00000000-0000-0000-0000-000000000000", None),
        ("operation retry missing", "POST", f"/v1/default/banks/{bank}/operations/00000000-0000-0000-0000-000000000000/retry", None),
        ("operation delete missing", "DELETE", f"/v1/default/banks/{bank}/operations/00000000-0000-0000-0000-000000000000/delete", None),
        # Webhooks.
        ("webhooks list", "GET", f"/v1/default/banks/{bank}/webhooks", None),
        ("webhooks limit 0", "GET", f"/v1/default/banks/{bank}/webhooks?limit=0", None),
        ("webhooks limit 1001", "GET", f"/v1/default/banks/{bank}/webhooks?limit=1001", None),
        ("webhook create no url", "POST", f"/v1/default/banks/{bank}/webhooks", json.dumps({"event_types": ["retain.completed"]})),
        ("webhook create bad url", "POST", f"/v1/default/banks/{bank}/webhooks", json.dumps({"url": "notaurl", "event_types": ["retain.completed"]})),
        ("webhook create private", "POST", f"/v1/default/banks/{bank}/webhooks", json.dumps({"url": "http://127.0.0.1/hook", "event_types": ["retain.completed"]})),
        ("webhook create", "POST", f"/v1/default/banks/{bank}/webhooks", json.dumps({"url": "https://hooks.example.com/p", "event_types": ["retain.completed"]})),
        ("webhook create unknown event", "POST", f"/v1/default/banks/{bank}/webhooks", json.dumps({"url": "https://hooks.example.com/q", "event_types": ["nonsense.event"]})),
        ("webhook patch missing", "PATCH", f"/v1/default/banks/{bank}/webhooks/00000000-0000-0000-0000-000000000000", json.dumps({"enabled": False})),
        ("webhook patch empty", "PATCH", f"/v1/default/banks/{bank}/webhooks/00000000-0000-0000-0000-000000000000", "{}"),
        ("webhook delete missing", "DELETE", f"/v1/default/banks/{bank}/webhooks/00000000-0000-0000-0000-000000000000", None),
        ("webhook delete bad id", "DELETE", f"/v1/default/banks/{bank}/webhooks/nope", None),
        ("webhook deliveries missing", "GET", f"/v1/default/banks/{bank}/webhooks/00000000-0000-0000-0000-000000000000/deliveries", None),
        ("webhook deliveries bad cursor", "GET", f"/v1/default/banks/{bank}/webhooks/00000000-0000-0000-0000-000000000000/deliveries?cursor=notatimestamp", None),
        ("webhook deliveries limit 201", "GET", f"/v1/default/banks/{bank}/webhooks/00000000-0000-0000-0000-000000000000/deliveries?limit=201", None),
        # Transfer family and retired routes.
        ("export template", "GET", f"/v1/default/banks/{bank}/export", None),
        ("export template ghost", "GET", f"/v1/default/banks/{ghost}/export", None),
        ("import template no body", "POST", f"/v1/default/banks/{bank}/import", "{}"),
        ("import template dry run", "POST", f"/v1/default/banks/{bank}/import?dry_run=true", json.dumps({"version": "1"})),
        ("document transfer retired", "GET", f"/v1/default/banks/{bank}/document-transfer", None),
        ("document transfer no multipart", "POST", f"/v1/default/banks/{bank}/document-transfer", json.dumps({})),
        ("document transfer export", "POST", f"/v1/default/banks/{bank}/document-transfer/export", None),
        ("transfer export nothing", "POST", f"/v1/default/banks/{bank}/transfer/export", None),
        ("transfer export ghost", "POST", f"/v1/default/banks/{ghost}/transfer/export", None),
        ("transfer import missing", "POST", f"/v1/default/banks/{bank}/transfer/import", None),
        ("clone missing target", "POST", f"/v1/default/banks/{bank}/clone", None),
        ("clone self", "POST", f"/v1/default/banks/{bank}/clone?target_bank_id={bank}", None),
        ("clone ghost source", "POST", f"/v1/default/banks/{ghost}/clone?target_bank_id={clone_target}", None),
        ("attachment missing", "GET", f"/v1/default/banks/{bank}/attachments/nope", None),
        ("download missing", "GET", "/v1/default/files/download/banks/b1/x.zip", None),
        ("download unclean", "GET", "/v1/default/files/download//banks/b1/x.zip", None),
        ("profile retired", "GET", f"/v1/default/banks/{bank}/profile", None),
        ("profile put retired", "PUT", f"/v1/default/banks/{bank}/profile", json.dumps({"disposition": {"skepticism": 3, "literalism": 3, "empathy": 3}})),
        ("background retired", "POST", f"/v1/default/banks/{bank}/background", json.dumps({"content": "c"})),
        # MCP transport.
        ("mcp without session", "POST", "/mcp", json.dumps({"jsonrpc": "2.0", "id": 1, "method": "tools/list"})),
        ("mcp unknown session", "POST", "/mcp", json.dumps({"jsonrpc": "2.0", "id": 1, "method": "tools/list"}),
         {"Mcp-Session-Id": "00000000-0000-0000-0000-000000000000"}),
        ("mcp bad method", "POST", "/mcp", json.dumps({"jsonrpc": "2.0", "id": 1, "method": "nope"})),
    ]


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--reference", required=True)
    parser.add_argument("--reference-key", default="")
    parser.add_argument("--service", required=True)
    parser.add_argument("--service-key", default="")
    parser.add_argument("--bank", default="")
    parser.add_argument("--other", default="")
    parser.add_argument("--run-id", default="",
                        help="suffix for the fixture banks (default: this process's pid, so "
                             "two probes against the same deployment cannot see each other's "
                             "fixtures)")
    parser.add_argument("--verbose", action="store_true")
    args = parser.parse_args()

    run_id = args.run_id or str(os.getpid())
    bank = args.bank or f"parity-probe-{run_id}"
    other = args.other or f"parity-probe-other-{run_id}"
    ghost = bank + "-ghost"
    clone_target = f"parity-clone-{run_id}"
    fixtures = (bank, other, ghost, clone_target)
    for base, key in ((args.reference, args.reference_key), (args.service, args.service_key)):
        for name in fixtures:
            request(base, "DELETE", f"/v1/default/banks/{name}", key=key)

    same = differing = known = 0
    for entry in checks(bank, other, ghost, clone_target):
        name, method, path, body = entry[:4]
        headers = entry[4] if len(entry) > 4 else None
        header_names = entry[5] if len(entry) > 5 else None
        want_status, want_body, want_headers, want_length = request_full(
            args.reference, method, path, body, args.reference_key, headers=headers
        )
        got_status, got_body, got_headers, got_length = request_full(
            args.service, method, path, body, args.service_key, headers=headers
        )
        if header_names is not None:
            want = (want_status, header_view(want_headers, want_length, header_names))
            got = (got_status, header_view(got_headers, got_length, header_names))
            matches = want == got
        else:
            want = (want_status, want_body)
            got = (got_status, got_body)
            if name == "banks list":
                want = (want[0], bank_list_shape(want[1]))
                got = (got[0], bank_list_shape(got[1]))
            if name in SHAPE_ONLY:
                matches = want[0] == got[0] and shape_of(want[1]) == shape_of(got[1])
            else:
                matches = want[0] == got[0] and normalise(
                    without_deployment_settings(want[1])
                ) == normalise(without_deployment_settings(got[1]))
        if matches:
            same += 1
            if args.verbose:
                print(f"SAME {method:6} {path}")
            continue
        if name in KNOWN_DIFFERENCES:
            known += 1
            print(f"KNOWN {name}: {method} {path}\n  why: {KNOWN_DIFFERENCES[name]}")
            continue
        differing += 1
        print(f"DIFF {name}: {method} {path}")
        print(f"  reference[{want[0]}]: {json.dumps(want[1], ensure_ascii=False)[:300]}")
        print(f"  service  [{got[0]}]: {json.dumps(got[1], ensure_ascii=False)[:300]}")

    for base, key in ((args.reference, args.reference_key), (args.service, args.service_key)):
        for name in fixtures:
            request(base, "DELETE", f"/v1/default/banks/{name}", key=key)

    total = same + differing + known
    print(f"\n{total} checks: {same} identical, {differing} differing, {known} known")
    return 1 if differing else 0


if __name__ == "__main__":
    sys.exit(main())