# WenYuan TypeScript Client

TypeScript client for the WenYuan agent-memory HTTP API.

The SDK under `generated/` is produced from this repository's own OpenAPI
document (`clients/openapi.json`) by `@hey-api/openapi-ts`, pinned in
`package.json`. `src/index.ts` is the hand-written façade: it re-exports the whole
generated surface and adds a configured `createClient()` plus convenience helpers
for the three core memory operations.

## Install

```bash
npm install wenyuan-client
```

## Usage

```ts
import { createClient, recall, reflect, retain } from "wenyuan-client";

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

// Search the bank.
const hits = await recall(client, "alice", "Where does Alice work?", { budget: "mid" });
console.log(hits.results);

// Ask for a synthesised answer.
const answer = await reflect(client, "alice", "What do we know about Alice?");
console.log(answer.text);
```

### Configuration

```ts
const client = createClient({
  baseUrl: "https://wenyuan.example.com", // otherwise HINDSIGHT_API_URL, otherwise http://localhost:18888
  apiKey: "…",                             // otherwise HINDSIGHT_API_KEY; omitted entirely when neither is set
  headers: { "x-request-id": "abc" },
  fetch: myFetch,                          // custom fetch, e.g. a stub in tests
});
```

`HINDSIGHT_API_URL` and `HINDSIGHT_API_KEY` are the deployment's spelling for the
API endpoint and key; the same names the command-line tool reads.

### Errors

The helpers resolve with the decoded payload. A request the server answered with
an error status throws a `WenYuanError` carrying `statusCode` and the parsed
`details`. The raw generated functions are still exported if you prefer the
`{ data, error, response }` result object:

```ts
import { createClient, recallMemories } from "wenyuan-client";

const client = createClient();
const { data, error, response } = await recallMemories({
  client,
  path: { bank_id: "alice" },
  body: { query: "Where does Alice work?" },
});
```

Every operation in the API is available this way — the generated SDK exposes one
function per operation, and the generated types cover the request and response
schemas.

## Cross-runtime notes

`createClient()` reads the environment through `process.env` when it is present
and simply sees no environment otherwise, so the same bundle runs under Node,
Bun and Deno (pass `baseUrl`/`apiKey` explicitly there, or set the variables in
the host). Requests use the global `fetch`, which all of those runtimes provide.

## Development

```bash
npm install
npm run generate    # rebuild generated/ from ../openapi.json
npm run typecheck   # tsc --noEmit
npm test            # vitest, no network access
npm run build       # bundle dist/ (ESM + CJS + type declarations)
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