# WenYuan TypeScript Client

TypeScript client for the WenYuan agent-memory HTTP API.

The SDK under `generated/` is produced from this repository's own OpenAPI
document (`clients/openapi.json`) by `@hey-api/openapi-ts`, pinned in
`package.json`. `src/index.ts` is the hand-written façade: it re-exports the whole
generated surface (as individual functions and an `sdk` namespace) and adds two
styles of client on top of it:

- **`WenYuanClient`** — a class with one method per bank operation
  (`retain`, `recall`, `reflect`, `listMemories`, bank profile/config, directives,
  mental models, the knowledge base, documents and the bank transfer flows),
  taking camelCase options and mapping them onto the request the API reads.
- **Free helpers** — `createClient()` plus `retain`, `retainBatch`, `retainFiles`,
  `recall`, `reflect` and `recallResponseToPromptString`, which take the
  generated options as they are.

Both run over the same generated SDK, so they compose, and neither departs from
the other in what it sends for the operations they share.

## Install

```bash
npm install wenyuan-client
```

## Usage

```ts
import {
  WenYuanClient,
  createClient,
  recall,
  recallResponseToPromptString,
  reflect,
  retain,
  retainBatch,
  retainFiles,
} from "wenyuan-client";

// Reads the base URL from WENYUAN_API_URL (default http://localhost:18888)
// and the bearer key from WENYUAN_API_KEY, when one is set.
const api = new WenYuanClient();

// Store a memory, then search the bank.
await api.retain("alice", "Alice works at the observatory.");
const hits = await api.recall("alice", "Where does Alice work?", { tags: ["people"] });

// A memory with more than the text: options carry context, metadata, tags and
// the document it belongs to.
await api.retain("alice", "Alice prefers the 3am shift.", {
  context: "scheduling",
  metadata: { source: "chat" },
  documentId: "shift-notes",
  tags: ["scheduling"],
});

// Multimodal content: an attachment sits inline where it appears.
await api.retain("alice", [
  { type: "text", text: "the error users see:" },
  { type: "image", source: { type: "base64", media_type: "image/png", data: "aGVsbG8=" } },
], { async: true });

// Whole-bank operations, each with camelCase options.
await api.createBank("alice", { reflectMission: "Know Alice's work." });
await api.updateBankConfig("alice", { entityLabels: [{ key: "name", type: "text" }] });
await api.createDirective("alice", "be-brief", "Answer in one sentence.");
await api.createMentalModel("alice", "Projects", "Which projects is Alice on?", {
  trigger: { tagsMatch: "any" },
});

// Export a bank as a transfer archive: the helper submits the async export,
// polls the operation and downloads the ZIP. The download of a signed URL goes
// to the URL itself, without this client's credentials.
const archive = await api.exportBank("alice", { includeData: true, includeBankConfig: true });
```

The free helpers are the same operations in their original shape:

```ts
const client = createClient();

await retain(client, "alice", "Alice works at the observatory.");
await retainBatch(client, "alice", [
  { content: "Alice loves hiking.", tags: ["outdoors"] },
  { content: "Alice visited Paris last summer." },
], { document_tags: ["chat-log"] });

// Upload files (PDF, DOCX, images, audio, ...): always asynchronous.
const upload = await retainFiles(client, "alice", [pdfBlob], {
  filesMetadata: [{ tags: ["reports"], document_id: "q3-report" }],
});
console.log(upload.operation_ids);

const hits = await recall(client, "alice", "Where does Alice work?", { budget: "mid" });
console.log(hits.results);

// Render the hits as a prompt for a downstream LLM.
const prompt = recallResponseToPromptString(hits);

// Ask for a synthesised answer.
const answer = await reflect(client, "alice", "What do we know about Alice?");
console.log(answer.text);
```

Where the two overlap they differ deliberately in option shape: the class takes
camelCase (`documentId`, `resolveEntities`, `updateMode`, `maxTokens`,
`tagsMatch`, `temporalWindow`, ...), the free helpers take the generated request's
own snake_case fields. `retainBatch` on the class also applies a batch-level
`documentId` to items that name none, and the class's `recall`/`reflect` apply the
documented default budget (`mid` / `low`) the way the API's own client does.

### Configuration

```ts
const api = new WenYuanClient({
  baseUrl: "https://wenyuan.example.com", // otherwise WENYUAN_API_URL, otherwise http://localhost:18888
  apiKey: "…",                             // otherwise WENYUAN_API_KEY; omitted entirely when neither is set
  userAgent: "acme-app/1.2.0",             // prefixed in front of wenyuan-client/<version>
  headers: { "x-request-id": "abc" },
  maxAttempts: 3,                          // retry budget for recall/reflect (default 3; 1 disables)
  fetch: myFetch,                          // custom fetch, e.g. a stub in tests
});
```

The class takes the same options object as `createClient()`, and reads the
environment the same way; `baseUrl` is therefore optional on both rather than
required. The class always consumes the generated client's
`{ data, error, response }` result shape, whatever `responseStyle` a caller sets.

`WENYUAN_API_URL` and `WENYUAN_API_KEY` are the deployment's spelling for the
API endpoint and key; the same names the command-line tool reads.

### Retries

`recall` and `reflect` are reads, so they are retried when the server reports it
is at capacity (HTTP `429`/`503`), up to the client's `maxAttempts`. The wait
honours `Retry-After` (delta-seconds form; an HTTP-date falls back to exponential
backoff) and is jittered, so a burst of clients does not wake in lockstep. Writes
are never retried — a repeated synchronous `retain` could duplicate a memory.
Every helper also accepts a per-call `{ signal }` to cancel the request and any
wait between attempts. `retryAfterMs` and `retryOnCapacity` are exported if you
need the policy directly.

### Errors

Both styles resolve with the decoded payload. A request the server answered with
an error status throws a `WenYuanError` carrying `statusCode` and the parsed
`details`; the class names the operation it failed on in the message. The bank
transfer helpers throw the same error when an export operation fails, times out
(`timeoutMs`, default 300000, polling every `pollIntervalMs`, default 2000) or
completes without naming an archive. The raw generated functions are still
exported if you prefer the `{ data, error, response }` result object:

```ts
import { createClient, sdk } from "wenyuan-client";

const client = createClient();
const { data, error, response } = await sdk.recallMemories({
  client,
  path: { bank_id: "alice" },
  body: { query: "Where does Alice work?" },
});
```

Every operation in the API is available this way — the generated SDK exposes one
function per operation (as both individual exports and the `sdk` namespace), and
the generated types cover the request and response schemas.

## Runtime support

This package ships **ESM and CJS builds for Node.js only** — `index.js` (ESM),
`index.cjs` (CJS) and their type declarations. There is no Deno target. Requests
use the global `fetch` (Node 18+), and `createClient()` reads the environment
through `process.env`; pass `baseUrl`/`apiKey` explicitly when the environment is
not available.

## Development

```bash
npm install
npm run generate    # rebuild generated/ from ../openapi.json
npm run typecheck   # tsc --noEmit
npm test            # vitest, offline: the live suite is skipped unless WENYUAN_API_URL is set
npm run build       # bundle dist/ (ESM + CJS + type declarations)
```

The live suite (`tests/live.test.ts`) runs only when `WENYUAN_API_URL` is set,
so the default run needs no server. Besides the core memory operations it covers
the convenience client: a bank's config round trip, its mission and its delete, a
directive and a mental model created and listed, and an export→download round trip
that checks the archive is a ZIP whose manifest names the bank:

```bash
# Against a server started with WENYUAN_API_DATABASE_URL=memory
WENYUAN_API_URL=http://127.0.0.1:18888 npm test
```

`npm run build` bundles through `tsup`, because `tsc` alone cannot produce a
loadable ESM tree: the façade deep-imports the generated sources, and Node's ESM
loader rejects an extensionless directory specifier. The bundle inlines them, so
`dist/` holds a self-contained `index.js` (ESM, `module`), `index.cjs` (CJS,
`main`), `index.d.ts` / `index.d.cts` (`types`), and source maps.

`generated/` is committed, so a fresh clone typechecks and tests without running
the generator. Regenerate it whenever `clients/openapi.json` changes; the
repository-level driver is `scripts/generate_clients.sh typescript`, which
rebuilds the OpenAPI document first.

The generated tree is the generator's output and is not edited by hand — any
project-specific behaviour belongs in `src/index.ts`.