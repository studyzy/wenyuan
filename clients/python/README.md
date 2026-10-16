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

### Anything the façade does not cover

The generated API objects are exposed unchanged, together with the response
models:

```python
client.memory        # generated MemoryApi
client.banks         # generated BanksApi
client.monitoring    # generated MonitoringApi
```

```python
from wenyuan_client import RecallResponse, RetainResponse, ReflectResponse
```

`recall`, `reflect`, `create_bank` and `aretain` accept extra keyword arguments
that are passed straight to the generated request model
(`RecallRequest`, `ReflectRequest`, `CreateBankRequest`, `MemoryItem`), so the
whole request surface is reachable without waiting for the façade to grow a
parameter.

## Tests

No network is used: the suite runs the wrapper against a loopback `http.server`
stub and checks both directions of the decode path.

```
cd clients/python
python3 -m unittest discover -s tests -v
```

(`pytest` also collects the same file if it is installed.)

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