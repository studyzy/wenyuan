# AGENTS.md — console

Guidance for AI coding agents working in the `console/` subtree.

## What this is

A self-contained Next.js App Router frontend for the `wenyuan` control plane.
It is a separate npm package with its own `package.json`; the repository-root
Go module does not read or build it.

## Commands (run from `console/`)

```sh
npm install          # one-time; dependencies are not committed
npm run typecheck    # tsc --noEmit
npm run lint         # eslint . (ESLint 9 flat config)
npm run test         # vitest run
npm run build        # next build
npm run dev          # next dev on port 9999
```

`npm run test` must pass offline. There is no shared make target for the
console, and the root `make check` does not cover it — run these two gates
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