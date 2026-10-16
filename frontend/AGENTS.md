# AGENTS.md — frontend

Guidance for AI coding agents working in the `frontend/` subtree.

## What this is

A self-contained Next.js App Router frontend for the `wenyuan` control plane.
It is a separate npm package with its own `package.json`; the repository-root
Go module does not read or build it.

## Commands (run from `frontend/`)

```sh
npm install          # one-time; dependencies are not committed
npm run typecheck    # tsc --noEmit
npm run lint         # eslint . (ESLint 9 flat config)
npm run test         # vitest run
npm run build        # next build
npm run dev          # next dev on port 19999
```

`npm run test` must pass offline. There is no shared make target for the
frontend, and the root `make check` does not cover it — run these two gates
separately.

## Gotchas

- **Async request APIs.** `cookies()` and `headers()` return promises and must
  be awaited. Dynamic-route `params` and page `searchParams` are also promises;
  await them before use. Route handlers type their context as
  `{ params: Promise<{ ... }> }`, and pages do the same.
- **The middleware runs on the Edge runtime.** It cannot import `node:crypto`.
  Session verification for middleware lives in `lib/server/session-edge.ts`
  (Web Crypto, HMAC-SHA256, constant-time compare). The Node equivalent used by
  route handlers is `lib/server/session.ts`. Keep the two token formats in sync.
- **Base path affects whitelist matching.** When `NEXT_PUBLIC_BASE_PATH` is set,
  the framework reports requests with that prefix attached. The access gate
  strips the base path before matching its fixed public-path whitelist, and the
  login redirect is base-path prefixed. Both must stay consistent or the login
  page becomes unreachable.
- **Locale is never a URL segment.** Locale resolution comes from the cookie
  pair then `Accept-Language`. Do not introduce locale path prefixes.
- **Message catalogs are paired.** Every locale in `i18n/locales.ts` needs a
  matching `messages/<locale>.json` with an identical key structure. The i18n
  test enforces parity.
- **Upstream credential stays server-side.** `HINDSIGHT_CP_DATAPLANE_API_KEY` is
  attached only by `lib/server/upstream.ts`; never reference it from
  `lib/client/` or any component.
- **Route handlers are unit-tested as plain functions.** `test/support/harness.ts`
  builds a standard `Request` and passes a context object, so tests run without
  a framework server. New handlers should follow the same shape.

## Layout

```
app/               pages, layout, and the API route tree
app/api/           server route handlers (grouped by concern)
lib/server/        upstream client, config, errors, session, paths, proxy
lib/middleware/    access gate, whitelist, matcher, locale policy
lib/client/        browser API layer and client state
i18n/ messages/    locale registry, negotiation, and per-locale catalogs
components/        shared UI primitives and chart wrappers
test/              vitest suites plus shared fixtures and harnesses
```

## File size budget

Code files stay under 500 lines, test files under 800. Message catalogs are the
only structural exception.

## Provenance: this tree is a copy of the reference frontend

The UI here is **copied from the reference control plane** (`../hindsight/hindsight-control-plane/src/**`)
rather than re-derived, by the owner's decision: "前端可以复制". So:

- When a view, dialog or store hook misbehaves, **read the reference file** and port the difference;
  do not invent a parallel implementation. Copy verbatim and adapt only import specifiers and local
  wiring.
- **Brand**: product-visible text stays ours (`wenyuan`, `WenYuan`, `文渊`); the upstream product name
  never appears. **Contract strings stay byte-identical** and are never "cleaned up": environment
  variable names (`HINDSIGHT_CP_*`), cookie names, and the health route's `SERVICE_NAME` — a
  deployment reads those.
- **Do not fork the plumbing.** `lib/server/**` (the upstream proxy), `lib/middleware/**` (access gate
  and locale), `lib/server/session.ts`, `lib/client/**` and `lib/use-async-data.ts` are ours: the
  reference's `@/lib/hindsight-client` (its generated SDK), `@/lib/auth/session` and
  `@/i18n/navigation` are not portable and must map onto ours. The locale set has one source of truth
  in `i18n/locales.ts`; `i18n/routing.ts` reads it.
- Copied files are exempt from the `react-hooks` rules in `eslint.config.mjs`'s scoped block, because
  the reference registers that plugin but enables none of its rules. Fix rather than exempt when the
  rule is pointing at real misbehaviour (a stale closure that changes output, an effect loop, a ref
  read during render) — `use-async-data.ts`'s request key was one of those, and every panel refetched
  forever until it was fixed.

## Gates

```sh
npm run typecheck && npm run lint && npm run test && npm run build   # from frontend/
```

`scripts/frontend_integration.sh` (repo root) is the cross-stack gate: it starts this repository's own
service on a private database, builds and starts this frontend with its dataplane pointed at it, and
then proves the things unit tests cannot — that the access gate admits and refuses the right callers,
that the BFF reads and writes through to the service, that a memory written through a frontend route is
visible through a frontend read route, and that the pages render server-side. Run it after any change to
a route handler, the proxy, the session/gate, or the page structure. Its checks print the offending
body on failure.

<!-- BEGIN:nextjs-agent-rules -->

## This is NOT the Next.js you know

This version has breaking changes — APIs, conventions, and file structure may all differ from your training data. Read the relevant guide in `node_modules/next/dist/docs/` (resolved from this file's directory; in monorepos the `next` package may not be visible from the repo root) before writing any code. Heed deprecation notices.

This block is written and re-added by `next dev` — verify at `node_modules/next/dist/server/lib/generate-agent-files.js`. Removing it from a diff only re-creates the uncommitted change; committing it with your work keeps the tree clean.

<!-- END:nextjs-agent-rules -->
