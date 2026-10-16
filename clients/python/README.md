# wenyuan-client (Python)

Python client for a **WenYuan** agent-memory server: retain memories, recall
them, and reflect a synthesised answer, over the service's HTTP API.

Two layers ship in this directory:

- `wenyuan_client_api/` — the generated OpenAPI client (package
  `wenyuan_client_api`), produced from `clients/openapi.json`.
- `wenyuan_client/` — a small hand-written façade (distribution
  `wenyuan-client`) exposing the common operations in sync and async form.

## Install

```
pip install wenyuan-client
```

From a checkout, install the local trees:

```
pip install clients/python
```

## Getting a server URL

`WenYuan()` reads the base URL from the environment:

| variable | meaning |
| --- | --- |
| `HINDSIGHT_API_URL` | Root URL of the server. Part of the deployment contract; read verbatim. Defaults to `http://localhost:18888` when unset. |

A trailing slash is tolerated. `api_key` is sent as a bearer token on every
request.

`WenYuan(...)` also takes:

| argument | default | meaning |
| --- | --- | --- |
| `base_url` | `HINDSIGHT_API_URL`, else `http://localhost:18888` | Server root. |
| `api_key` | `None` | Bearer token sent with every request. |
| `timeout` | `300.0` | Per-request timeout in seconds. |
| `user_agent` | `None` | Extra product token placed in front of this client's own in `User-Agent`. |
| `max_attempts` | `3` | Total attempts an idempotent call (recall, reflect) makes when the server is at capacity. |

## Use

```python
from wenyuan_client import WenYuan

with WenYuan() as client:  # HINDSIGHT_API_URL, else http://localhost:18888
    client.create_bank("ops", name="Operations")

    # retain: a plain string, or an ordered list of content blocks
    client.retain("ops", "the deploy runs on Fridays")
    client.retain(
        "ops",
        [
            {"type": "text", "text": "look at this screenshot:"},
            {
                "type": "image",
                "source": {"type": "base64", "media_type": "image/png", "data": "..."},
            },
        ],
    )

    # recall: results relevant to a query
    hits = client.recall("ops", "when do we deploy?", max_tokens=512)
    for hit in hits.results:
        print(hit.text, hit.scores)

    # reflect: an answer synthesised from the bank's memories
    answer = client.reflect("ops", "summarise the deploy policy")
    print(answer.text)

    for bank in client.list_banks(limit=20).banks:
        print(bank.bank_id, bank.fact_count)
```

### Async

Every operation has an awaitable counterpart — `aretain`, `arecall`,
`areflect`, `acreate_bank`, `alist_banks` — and the client is an async context
manager:

```python
import asyncio

from wenyuan_client import WenYuan


async def main():
    async with WenYuan() as client:
        await client.aretain("ops", "the deploy runs on Fridays")
        hits = await client.arecall("ops", "when do we deploy?")
        answer = await client.areflect("ops", "summarise the deploy policy")
        print(answer.text, len(hits.results))


asyncio.run(main())
```

Calling a synchronous method from inside a running event loop raises
`RuntimeError` rather than deadlocking; use the `a`-prefixed method there.

### Suspending retains

`suspend_retains()` suppresses writes for the current flow or task, so a session
can read a bank without adding to it (evaluating against a populated bank,
replaying a transcript, and so on). Recall and reflect are unaffected, while
`retain`, `retain_batch` and `retain_files` (and their awaitable forms) send no
request and report an empty result — `items_count=0`, no operation ids:

```python
with client.suspend_retains():
    client.retain("ops", "not stored")          # items_count == 0
    hits = client.recall("ops", "still works")  # unaffected
```

Scopes may be nested and are restored on exit, including after an exception.
The suspension is local to the current execution context, so concurrent users of
the same client are unaffected. Only the convenience methods are guarded; the
low-level accessors (`client.memory`, `client.files`) call the generated API
directly.

### Retry on capacity

`recall` and `reflect` are reads, so when the server reports it is at capacity
(`429`/`503`) they retry up to `max_attempts` times (default 3; `1` disables
retrying). The wait honours the server's `Retry-After` when it sends one, and is
otherwise a backoff starting at 0.5s; either way it is jittered, so a burst of
clients does not come back in lockstep and rebuild the spike. The final failure
surfaces the original error unchanged. Writes are never retried — a duplicated
retain is worse than a refused one.

### Anything the façade does not cover

The generated API objects are exposed unchanged as properties, one per area:

| property | area |
| --- | --- |
| `client.memory` | retain, recall, reflect, memories, tags, graph |
| `client.banks` | bank lifecycle, stats, consolidation, config |
| `client.documents` | documents and chunks |
| `client.entities` | entities, observations, entity graph |
| `client.mental_models` | mental-model CRUD, refresh, history |
| `client.knowledge_base` | folder/page tree, page CRUD, search, export |
| `client.directives` | directive CRUD |
| `client.operations` | async operation status, list, cancel, retry |
| `client.webhooks` | webhooks and deliveries |
| `client.files` | file upload and retain |
| `client.monitoring` | health, liveness, readiness, version, metrics |
| `client.document_transfer` | document export/import, file download |
| `client.bank_transfer` | whole-bank export/import, clone |
| `client.audit` | audit log entries and stats |
| `client.bank_templates` | template export/import, schema |
| `client.llm_traces` | LLM request traces and stats |

Every method on those objects is async.

```python
from wenyuan_client import RecallResponse, RetainResponse, ReflectResponse
```

`recall`, `reflect`, `create_bank` and `aretain` accept extra keyword arguments
that are passed straight to the generated request model
(`RecallRequest`, `ReflectRequest`, `CreateBankRequest`, `MemoryItem`), so the
whole request surface is reachable without waiting for the façade to grow a
parameter.

### Recall-response helpers

A recall response carries the matched facts, the chunks they came from and the
entity summaries, but as a bare generated model reaching into it means walking
`results` by hand every time. The wrapper adds the projection, so a caller can
go straight to a prompt:

```python
from wenyuan_client_api.models.chunk_include_options import ChunkIncludeOptions
from wenyuan_client_api.models.include_options import IncludeOptions

hits = client.recall("ops", "when do we deploy?", include=IncludeOptions(chunks=ChunkIncludeOptions()))
print(hits.to_prompt_string())   # "FACTS:" as JSON (+ source chunks), then "ENTITIES:"
for hit in hits:                 # iterate
    print(hit)
len(hits)                        # number of results
hits[0]                          # index into them
repr(hits), repr(hits[0])        # short previews
```

The same capability is available as module-level functions —
`to_prompt_string(response)`, `iter_results(response)`, `result_count(response)`,
`result_at(response, index)` — for callers that prefer not to rely on patched
methods.

The generated package is never edited, so these attributes are attached by the
wrapper at import time: a caller that imports only `wenyuan_client_api` sees the
bare models, and importing `wenyuan_client` is what opts into the helpers. That
is the one deliberate difference from a client whose generated module carries
them directly.

## Tests

Two suites run under one command. The offline one uses no network — it drives the
wrapper against a loopback `http.server` stub, a mocked API object, and pure
model round-trips.

```
cd clients/python
python3 -m unittest discover -s tests -v
```

The live suite in `tests/test_live.py` is gated on `HINDSIGHT_API_URL`: unset,
those tests skip (so the command above stays green with no server); set, they
run against a real service — bank lifecycle, retain then document/chunk reads, a
tagged write selected back by its own tag and skipped by an unwritten one, an
async retain naming a queued operation, recall, reflect, entity/graph agreement,
and the retired profile route answering `410`.

```
HINDSIGHT_API_URL=http://127.0.0.1:18888 python3 -m unittest discover -s tests -v
```

A server started with `HINDSIGHT_API_DATABASE_URL=memory` is enough: with no
model provider the assertions are structural, not about extracted facts or
synthesised prose.

(`pytest` also collects the same files if it is installed.)

## Regeneration

The generator owns `wenyuan_client_api/`; everything else here is hand-written.
Rebuild the derived document and regenerate from the repo root:

```
scripts/generate_clients.sh python
```

That runs `scripts/derive_client_spec.py` (which writes `clients/openapi.json`
from the served document) and then invokes the pinned openapi-generator
(`v7.10.0`, `python` generator, `asyncio` library) with
`openapi-generator-config.yaml`.

### Two documented patches to the generated files

Regeneration overwrites `wenyuan_client_api/`, so the generator is followed by
`scripts/patch_generated.py`, which `generate_clients.sh python` calls
automatically right after generation. It applies exactly two edits and is
idempotent:

1. **`wenyuan_client_api/rest.py` — defer the aiohttp session.** The generated
   `RESTClientObject.__init__` builds an `aiohttp.TCPConnector`, which binds the
   running event loop; constructing a client outside a loop therefore raised
   `no running event loop`. The patch stores the configuration in `__init__` and
   builds the session lazily in `_ensure_session()` on the first request
   (always async), exposing `pool_manager` / `retry_client` as properties so the
   rest of the generated file is unchanged.

2. **`wenyuan_client_api/models/content.py` — serialise the block list.** The
   generated `Content.to_dict` unwraps only a single object; the patch adds a
   branch that serialises each element of a list through its own `to_dict` when
   it has one. The wrapper additionally constructs `Content` positionally so a
   block list is carried verbatim as plain dicts (see `wenyuan_client/client.py`).

Do not edit generated files by hand: add the transformation to
`scripts/patch_generated.py` instead.