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

### Retain options

`retain` names the fields a retained item carries. The three the contract spells
as unions are written the way the contract writes them and wrapped for the
request model, so the plain value is accepted:

```python
client.retain(
    "ops",
    "the rollback window closed on Friday",
    timestamp="2024-01-15T10:30:00",         # ISO-8601 string, datetime, or "unset"
    context="release review",
    document_id="release-2024-01-15",        # groups items; batch-wide on retain_batch
    metadata={"source": "postmortem"},
    entities=[{"text": "release-2.3", "type": "release"}],  # or bare names
    resolve_entities=False,
    tags=["release"],
    update_mode="append",                    # "replace" or "append"
    observation_scopes="per_tag",            # mode name, or [[tag, ...], ...]
    strategy="named-strategy",
)
```

`operation_id` makes an **asynchronous** retain idempotent
(`retain_async=True`); on a synchronous request it is dropped with a warning
rather than sent, since there is nothing for the server to deduplicate against.
On `retain_batch`, `document_id` fills in for the items that do not name their
own, and `document_tags` applies to the whole batch.

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

### Reading and configuring a bank

```python
version = client.get_version()                        # api_version + feature flags

client.create_bank(
    "ops",
    name="Operations",
    disposition={"skepticism": 4, "literalism": 2, "empathy": 3},  # the legacy mapping
    retain_extraction_mode="verbose",
    enable_reranking=False,                           # omitted settings keep the server default
)

page = client.list_memories("ops", time_field="mentioned_at", limit=50)
config = client.get_bank_config("ops")                # plain dict: bank_id, config, overrides
client.update_bank_config("ops", enable_text_search=False)   # only the named settings are sent
client.reset_bank_config("ops")
client.delete_bank("ops")
```

`create_bank` takes the bank's configuration as named arguments: `name`,
`mission`, `reflect_mission`, `background`, the three `disposition_*` fields (or
the legacy `disposition` mapping, whose `skepticism`/`literalism`/`empathy` keys
are expanded into them — an individual field wins over the mapping), and the
retain / observation / recall toggles. Only the arguments you name are sent, so
an omitted field keeps the server's default. `set_mission` and
`set_reflect_mission` remain as the deprecated aliases of `create_bank(mission=...)`.
`update_bank_config` follows the same rule: only the settings you name reach the
request body.

`list_memories` enumerates its filters rather than passing a dict through, so a
filter a caller names always reaches the query: `search_query` (the `q`
parameter), `entity_id`, `document_id`, `state`, `consolidation_state`, the tag
filter, and the time window (`time_field` with `start_date`/`end_date`, a
half-open range that both filters and orders). Leaving the window unset keeps
the endpoint's own ordering and drops nothing.

### Mental models, knowledge base and directives

```python
model = client.create_mental_model("ops", name="Housing", source_query="Where does Alice live?")
client.update_mental_model("ops", model.mental_model_id, trigger={"mode": "delta"})
client.aget_mental_model_history  # (await) the change history, most recent first
client.delete_mental_model("ops", model.mental_model_id)

folder = client.create_knowledge_folder("ops", "Operations")
page = client.create_knowledge_page("ops", name="Deploying", source_query="How is it deployed?")
client.update_knowledge_node("ops", page.page_id, name="Deployment")   # partial patch
client.search_knowledge_base("ops", "deployment")
client.export_knowledge_base("ops")

directive = client.create_directive("ops", name="Tone", content="Be terse")
client.list_directives("ops")
client.update_directive("ops", directive.id, content="Be concise")
client.delete_directive("ops", directive.id)
```

Every family is present under the same names in both forms — `a`-prefixed for
async — and a sync/async pair always takes the same arguments.

A supplied `trigger` (mental models, knowledge pages and `update_knowledge_node`)
is a **patch**: the wrapper sends only the keys you named, so the settings you
left out keep their server-side default or stored value rather than being reset
to the model's default. Passing a nullable field explicitly (`{"refresh_cron":
None}`) still clears it. Likewise `update_knowledge_node` only sends the fields
you pass, so `parent_id=None` moves a node to the root while omitting it leaves
the parent alone.

### Bank transfer

Transfers run server-side as background operations. The wrapper submits, polls
the operation to a terminal state, and downloads the resulting archive:

```python
archive = client.export_bank("ops")                    # whole bank → bytes
archive = client.export_bank("ops", include_history=True, poll_interval=0.5, timeout=120)
archive = client.export_documents("ops", document_ids=["doc-1"])   # → bytes

operation_id = client.import_bank("ops", archive, target_bank_id="ops-copy")
operation_id = client.clone_bank("ops", "ops-clone")   # copy without an archive on the wire
```

An export raises `TimeoutError` if the operation does not finish within
`timeout` (the deadline bounds both the status request and the poll sleep), and
`RuntimeError` if it fails, is cancelled, or completes without naming an archive.
The archive is then fetched from the location the operation names: a signed
object-store URL is fetched directly, preserving its query string and sending no
API credentials; a server-relative URL goes through the client, keeping the
client's headers.

### Bank routes and path encoding

The bank and configuration routes are not hand-written here — they call the
generated client, which substitutes a bank id as a path parameter with no safe
characters (`quote(..., safe="")`). A bank id holding `?`, `#`, `/`, `+` or a
space therefore stays one path component: it cannot truncate the path or start a
query string. `tests/test_bank_routes.py` pins that round-trip (create, read
config, delete) together with the client identity reaching those routes. Setting
`api_key` attaches `Authorization: Bearer <key>` to every request, including the
routes whose generated definition declares no security scheme.

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

### Request options

`recall` and `reflect` name the options a caller reaches for, and every
remaining field of the generated request model stays reachable under its own
name. Response types are re-exported from the package root — `RecallResponse`,
`RetainResponse`, `ReflectResponse`, `ReflectFact`, `DispositionTraits`, and the
`*ListResponse`/`VersionResponse` models:

```python
# evidence attached to the response, bounded by its own token budget
hits = client.recall("ops", "when do we deploy?", include_entities=True, max_entity_tokens=200)

# boolean tag-rule trees and per-stage score floors
hits = client.recall(
    "ops",
    "when do we deploy?",
    tag_groups=[{"tags": ["customer"], "match": "all"}, {"not": {"tags": ["internal"]}}],
    min_scores={"semantic": 0.2, "final": 0.5},
)

# the memories the answer was built from, and the tool/LLM trace
answer = client.reflect("ops", "summarise the deploy policy", include_facts=True)
answer = client.reflect("ops", "summarise the deploy policy", include_tool_calls=True)
```

The include flags are shorthand for the `include` object the request carries, so
the two spellings are alternatives — passing both raises `ValueError`:

```python
from wenyuan_client_api.models.chunk_include_options import ChunkIncludeOptions
from wenyuan_client_api.models.include_options import IncludeOptions

hits = client.recall("ops", "when do we deploy?", include=IncludeOptions(chunks=ChunkIncludeOptions()))
```

Two kinds of mistake are refused instead of silently sent, because the generated
models drop a field they do not recognise: an option name the request model does
not have (`TypeError`), an unknown `min_scores` stage or item key (`ValueError`,
`TypeError`). `create_bank` names its arguments too; any further keyword argument
is still placed on the generated `CreateBankRequest`. `aretain` and `arecall`
carry the same options as their synchronous forms.

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

The generated package is never edited by hand: the recall attributes are
attached by the wrapper at import time, so a caller that imports only
`wenyuan_client_api` sees the bare models and importing `wenyuan_client` is what
opts into the helpers. The handful of edits the generated code itself needs (the
lazily built HTTP session, the two `anyOf` serialisation fixes) live in
`scripts/patch_generated.py`, which re-applies them after every regeneration and
is the place to look for what differs and why.

## Tests

Two suites run under one command. The offline one uses no network — it drives the
wrapper against loopback stub servers, mocked API objects, and pure model
round-trips. Alongside the retain/recall/reflect, retry, suspension and
sub-client tests, it covers the convenience families (`get_version`,
`list_memories`, bank create/config/delete, mental models, knowledge base,
directives), the sync/async pairing of every convenience method, the transfer
orchestration (submit → poll → download, timeout bounds, failure paths, and the
credential boundary between signed and server-relative downloads), and the
bank-route path encoding.

```
cd clients/python
python3 -m unittest discover -s tests -v
```

The live suite in `tests/test_live.py` is gated on `HINDSIGHT_API_URL`: unset,
those tests skip (so the command above stays green with no server); set, they
run against a real service — bank lifecycle, retain then document/chunk reads, a
tagged write selected back by its own tag and skipped by an unwritten one, an
async retain naming a queued operation, recall, reflect, entity/graph agreement,
the retired profile route answering `410`, the version and bank-config
conveniences, the mental-model / knowledge-base / directive families, and the
transfer flows (whole-bank export then import of the archive, document export,
and clone).

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