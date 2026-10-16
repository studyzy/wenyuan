# WenYuan TypeScript Client

TypeScript client for the WenYuan agent-memory HTTP API.

The SDK under `generated/` is produced from this repository's own OpenAPI
document (`clients/openapi.json`) by `@hey-api/openapi-ts`, pinned in
`package.json`. `src/index.ts` is the hand-written façade: it re-exports the whole
generated surface (as individual functions and an `sdk` namespace) and adds a
configured `createClient()` plus convenience helpers for the core memory
operations (`retain`, `retainBatch`, `retainFiles`, `recall`, `reflect`).

## Install

```bash
npm install wenyuan-client
```

## Usage

```ts
import {
  createClient,
  recall,
  recallResponseToPromptString,
  reflect,
  retain,
  retainBatch,
  retainFiles,
} from "wenyuan-client";

// Reads the base URL from HINDSIGHT_API_URL (default http://localhost:18888)
// and the bearer key from HINDSIGHT_API_KEY, when one is set.
const client = createClient();

// Store a memory.
await retain(client, "alice", "Alice works at the observatory.");

// A memory item, when you need more than the text.
await retain(
  client,
  "alice",
  { content: "Alice prefers the 3am shift.", context: "scheduling", metadata: { source: "chat" } },
  { async: true },
);

// Many memories in one call.
await retainBatch(
  client,
  "alice",
  [
    { content: "Alice loves hiking.", tags: ["outdoors"] },
    { content: "Alice visited Paris last summer." },
  ],
  { document_tags: ["chat-log"] },
);

// Upload files (PDF, DOCX, images, audio, ...): always asynchronous.
const upload = await retainFiles(client, "alice", [pdfBlob], {
  filesMetadata: [{ tags: ["reports"], document_id: "q3-report" }],
});
console.log(upload.operation_ids);

// Search the bank.
const hits = await recall(client, "alice", "Where does Alice work?", { budget: "mid" });
console.log(hits.results);

// Render the hits as a prompt for a downstream LLM.
const prompt = recallResponseToPromptString(hits);

// Ask for a synthesised answer.
const answer = await reflect(client, "alice", "What do we know about Alice?");
console.log(answer.text);
```

### Configuration

```ts
const client = createClient({
  baseUrl: "https://wenyuan.example.com", // otherwise HINDSIGHT_API_URL, otherwise http://localhost:18888
  apiKey: "…",                             // otherwise HINDSIGHT_API_KEY; omitted entirely when neither is set
  userAgent: "acme-app/1.2.0",             // prefixed in front of wenyuan-client/<version>
  headers: { "x-request-id": "abc" },
  maxAttempts: 3,                          // retry budget for recall/reflect (default 3; 1 disables)
  fetch: myFetch,                          // custom fetch, e.g. a stub in tests
});
```

`HINDSIGHT_API_URL` and `HINDSIGHT_API_KEY` are the deployment's spelling for the
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

The helpers resolve with the decoded payload. A request the server answered with
an error status throws a `WenYuanError` carrying `statusCode` and the parsed
`details`. The raw generated functions are still exported if you prefer the
`{ data, error, response }` result object:

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
npm test            # vitest, offline: the live suite is skipped unless HINDSIGHT_API_URL is set
npm run build       # bundle dist/ (ESM + CJS + type declarations)
```

The live suite (`tests/live.test.ts`) runs only when `HINDSIGHT_API_URL` is set,
so the default run needs no server:

```bash
# Against a server started with HINDSIGHT_API_DATABASE_URL=memory
HINDSIGHT_API_URL=http://127.0.0.1:18888 npm test
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