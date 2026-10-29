# 07 — DSH plugin review: `integrations/dsh-plugin` + `coding-agents` DSH wiring

Read line by line: `integrations/dsh-plugin/src/{index,client,settings,transcript,
transcript-projection,context-blocks,source}.ts`, `browser/{index,settings-controller}.ts`,
`cordis.patch.yml`, `package.json`; `coding-agents/src/hosts/{dsh,dsh-plugin,lifecycle,skills,
skill-locations}.ts`, `src/mcp/{register,mechanisms,json-server-map}.ts`,
`src/runtime/{mcp-server,skill-refresh}.ts`, `README.md`, `skill/SKILL.md`.
Reference: upstream `src/dsh.ts`, `cordis.patch.yml`, `plugin.json`, `index.js`,
`src/core/{session-start,retain-hook,knowledge-injection,knowledge-tools,config}.ts`.
Harness facts checked against the installed `/data/home/devinyzeng/.dsh-global/node_modules`
(`dsh 0.2.2-alpha.2`) and the plugin's pinned peers (`0.2.0-rc.2`); server facts against
`internal/mcp/*.go` and `internal/{memoryops,reporting,httpx}`.

**Process note.** `team_task_get` / `team_task_update` are not in this session's toolset, so
task-4 could not be claimed or completed. Nothing outside this file was modified; the throwaway
probe specs used as evidence were removed (`git status integrations/dsh-plugin` clean, 63/63 tests).

---

## 1. What the plugin does, end to end

### 1.1 Seams bound (`index.ts`)

```ts
63: export const inject = ['tools', 'sessionProjections', 'credentials']
216: ctx.on('agent/created', async ({ agent, signal }) => { ... })
226: ctx.on('agent/pre-step', async ({ agent, messages, turn, signal }, next) => { ... })
247: ctx.on('agent/turn-stopping', async ({ agent, signal }) => { ... })
260: ctx.effect(() => ctx.tools.register(tool), `dsh-wenyuan-memory: ${tool.name} tool`)
```

| Seam | Mode (harness) | What the plugin does |
|---|---|---|
| `agent/created` | `serial`, awaited, **throw fails creation** | one recall of the fixed `SESSION_START_QUERY` (`:88`) → `agent.inject(<wenyuan_memory>…)` (`:222`) |
| `agent/pre-step` | `waterfall` | once per `turn`: recall the prompt text, `next()` first, then append the block to `downstream.messages` when `kind === 'enter'` (`:238-243`) |
| `agent/turn-stopping` | `serial`, awaited | slice the `wenYuanTranscript` fold past `retainedTurns` → one `POST /memories` with `document_id = conversation:<sessionId>` |
| `ctx.tools.register` × 8 | per-profile, names visible to every agent | the `wenyuan_*` tools |
| `sessionProjections.register` | awaited at `apply` | the host-only `wenYuanTranscript` fold |
| `credentials.resolve` | per request | `WENYUAN_API_KEY`, then `WENYUAN_API_TOKEN` (`:159`) |
| Settings card | browser bundle | `plugins.item` + `plugins.bundle.config` seats, one shared face (`browser/index.ts:92`) |

No `agent/disposed` listener, no `session/event` listener, no bus listener, no timer job.

### 1.2 Recall path

`clientNow()` (`:188-194`) resolves credentials then builds a `WenYuanClient` whose base is
`${apiUrl}/v1/default/banks/${bankId}` (`settings.ts:62`). `WenYuanClient.request()` (`client.ts:324`)
aborts on `timeoutMs` **or** the caller's `signal` via `AbortSignal.any` (`client.ts:332`), sends
`authorization: Bearer …` only when a key resolved (`:335`), and maps non-2xx to
`{ok:false, reason:'HTTP <status>'}` (`:343`). `textsFromRecall` (`:163`) reads `results[].text`,
tolerating the older `memories[]` shape. Both lifecycle paths `.catch(() => null)` the recall
(`index.ts:220,235`), so a recall failure degrades to "no block".

### 1.3 Write-back path

`writeBack()` (`index.ts:321-352`): read the fold via the projection handle, slice
`folded.slice(perSession.retainedTurns)`, render with `renderSessionDocument` (`transcript.ts:139`,
a `REF-ID:` system line + NDJSON turns), and retain one item with
`context: RETAIN_CONTEXT`, `tags: ['source:chat','harness:dsh']`, `strategy:'conversation'`,
`timestamp`, string-valued `metadata`. Cursor advances **only** on `outcome.ok` (`:351`).
Concurrency: a newer stop awaits the in-flight write first (`:252`).

### 1.4 The eight tools (`index.ts:437-649`)

`wenyuan_search_knowledge_pages` (`query`,`limit`), `wenyuan_list_knowledge_pages`,
`wenyuan_read_knowledge_page` (`page_id`), `wenyuan_capture_initiative` (`title`,`summary`,
`relates_to_page_id`), `wenyuan_ingest_document` (`title`,`content`), `wenyuan_reflect` (`query`),
`wenyuan_diagnose`, `wenyuan_sync_status`.

### 1.5 Settings card

`settings-controller.ts` stages four section fields through `SettingsFormModel` plus one secret
writer that calls `ctx.remote.credentials.set('WENYUAN_API_KEY', …)` (`:223`) and re-reads
`describe()` for the "configured" badge (`:191`). The Host resolves the key fresh on every request,
so a rotated key is picked up without a reload.

### 1.6 Contract checks against the server — all pass

* Routes exist: `/memories/recall`, `/memories`, `/reflect`, `/stats`, `/operations`,
  `/knowledge-base/{tree,search,pages/{id}}` (`internal/httpx/api.go:34,69-76`).
* Envelopes match: `RecallResponse.Results []… json:"results"` (`memoryops/models.go:405`),
  `KnowledgeTreeResponse.Roots` (`reporting/knowledge_models.go:33`),
  search `Results`/`Snippet` (`:96-108`); search takes `q` + `limit` (`reporting/knowledge_search.go:96`).
* Retain body matches: `items`, `async` (`memoryops/retain_models.go:13`, `operation_models.go:12`);
  `content` is typed `Content`, which accepts a plain string.
* **Metadata values are all strings** — required, because the reference's metadata is
  `dict[str, str]` and a non-string is rejected (`memoryops/retain_models.go:49-53,178`). The plugin
  complies at `index.ts:346,534,568`.
* Tool names deliberately do **not** mirror MCP: MCP names are `recall`, `retain`, `reflect`,
  `search_knowledge_base`, `get_knowledge_base_tree`, `get_knowledge_page`,
  `create_knowledge_page`, `get_bank_stats`, `list_operations` (`internal/mcp/tools_defs_*.go`), with
  no `wenyuan_` prefix. The plugin's names cannot collide, but a model that learned the MCP
  vocabulary will call `recall`/`retain` — which only exist if the MCP server is also wired.

---

## 2. Correctness / robustness

Severity: **blocker** = memory wrong, lost, or the session harmed; **major** = documented
behaviour silently broken; **minor** = hygiene.

### B1 — blocker — recall is not per-workspace (one process, many repos) — `index.ts:170,191`

```ts
170:  const resolved = resolveSettings(config)          // once per profile
191:  connection: { apiUrl: resolved.apiUrl, bankId: resolved.bankId, ... }
```

`apply` resolves `bankId` **once** for the whole process and never reads `agent.session.header.cwd`.
Upstream is explicit that this is wrong: *"ONE dsh PROCESS SERVES MANY WORKSPACES… Memory is per-repo,
so a RuntimeCore (bank, client, seed) is built PER WORKSPACE ROOT and cached, never once per process"*
(`dsh.ts:10-15`), enforced by `workspaceRoot()` (`dsh.ts:162`) and by the per-call rebind in the tool
body (`dsh.ts:372-376`). Consequence: in the Web UI, a session opened on repo B recalls repo A's
bank, and its stop writes repo B's conversation into it. Memory is silently contaminated across
every repository the user opens in one `dsh` process.

### B2 — blocker — a thrown recall hangs the stop path for the whole transport budget — `index.ts:235`

```ts
235:    const outcome = await client.recall(query, signal).catch(() => null)
```

The `.catch` only guards the returned promise. `clientNow()` is awaited unguarded on `:233`, and the
10 s deadline lives **inside** `WenYuanClient.request` — so `fetch`/`response.text()` run outside
any timer. A body that trickles bytes (proxy, half-open socket) never trips `setTimeout`, because
`abort()` cannot fire mid-`text()` once headers arrived. `agent/turn-stopping` is serial and awaited
by the loop (`dsh-agent-loop/lib/index.js:791-794`), so every later stop of that process queues
behind it. The class doc claims "Everything here is fail-open" (`:22`); the read deadline does not
cover the response body.

### B3 — major — a throwing client **does** break the seam (no outer guard) — `index.ts:216-223, 226-244`

`resolveApiKey`'s own read is guarded (`:277`) and I confirmed empirically that a throwing
`credentials.resolve` still resolves the handler. But nothing else is:

* `clientNow` (`:190`) runs `new WenYuanClient({…})` → `bankUrl` → `normalizeApiUrl(...).replace`
  (`settings.ts:78`). A non-string field (a `volatile()` wrapper whose `get()` returns an object; a
  schema-drifted profile entry) throws a `TypeError` **before** any `.catch` exists — every call
  site catches the returned promise, not the construction (`:233,334`).
* `agent.inject(...)` (`:222`) is a host call inside the listener and is unguarded.

`agent/created` is `serial` and its contract is explicit: *"A throw or rejection fails creation"*
(`dsh-agent/runtime-types.d.ts:250`; `agents.announce` awaits `ctx.serial(...)` and cordis `serial`
propagates, `cordis/lib/index.js:289-293`). So one bad settings value turns memory off **and**
prevents every new session from starting. There is no top-level `try/catch` anywhere in `apply`,
and no test drives a throwing seam — `tests/plugin.spec.ts:86` only asserts the listener names.

### B4 — major — an aborted turn latches "already recalled" for the turn — `index.ts:230`

```ts
229:    if (perSession.recalledTurn === turn) return next()
230:    perSession.recalledTurn = turn
```

The marker is set *before* `clientNow`/`recall`. If the turn is cancelled while the recall is in
flight (`signal` abort ⇒ `request` returns `{ok:false}`, `client.ts:332,353`), or the service is
down, the turn is marked done and **no step of that turn ever recalls again** — including the step
after the user's stop-and-resend in the same turn. The comment at `:11-12` ("Each turn recalls at
most once") is right; the failure mode (a failed recall still counts) is not. Upstream keys on the
prompt text and only injects on success (`dsh.ts:264-282`).

### B5 — major — session-start recall fires again on resume — `index.ts:216-220`

1. `agent/created` (`:220`) recalls `SESSION_START_QUERY` and injects — on **every** creation.
2. A **resumed** session re-fires `agent/created` with `source: "resume"`
   (`dsh-agent-loop/lib/index.js:1765`; `agents.register` announces `"startup"`,
   `dsh-agent/lib/index.js:623`). `isTopLevel` never looks at `source`, so reopening a Web-UI session
   pays another service round trip and re-injects an identical block into history that already
   carries one.
3. Compaction is the mirror image: a new `Agent` gives a fresh `perSession` (`recalledTurn = -1`), so
   the first post-compaction prompt recalls again — correct — but no session-start block is
   re-injected, so the session loses its start-of-session context for good.

### B6 — major — the write-back cursor races itself — `index.ts:252-255, 332, 351`

```ts
252:    if (perSession.writing !== undefined) await perSession.writing.catch(() => {})
254:    perSession.writing = write
```
`write` is the promise of `writeBack(...)`; between `:252` and `:254` the previous `write` may still
be in its post-`await` continuation. More concretely, `folded` is captured at `:331` and
`retainedTurns` assigned at `:351` — a stop that begins after the previous stop's `retain()` call
but before its assignment slices the *same* `fresh` turns and retains them a second time under the
same `document_id`. Sequential turns usually order correctly, but a fast synthetic driver
(goal/ralph continuations, tools with `concludesTurn`) can interleave. The cursor should be claimed
(`const from = perSession.retainedTurns; perSession.retainedTurns = folded.length`) **before** the
await, and rolled back on failure.

### B7 — major — the fold ignores surface replacement, so compaction both doubles and loses turns — `transcript-projection.ts:91-99`

```ts
91:    if (event.type !== 'user/message' && event.type !== 'assistant/message') return state
99:    return [...state, ...appended]
```

`Session.append` carries `surfaceOp` and compaction rewrites the surface with
`{op:'replace', startSeq, endSeq}` citing `sourceEventSeqs` (`dsh-session/lib/types/types.d.ts:455-470`).
The fold appends every message event and never removes a shadowed one. So after a compaction the
transcript contains *both* the original turns and the summary/duplicate copies. Two follow-on
effects: (a) the next stop writes turns that were already written under the same `document_id`,
re-extracting them; (b) `tool/result` (`types.d.ts:382`) is never read, so a tool *failure* is
indistinguishable from success in the action line — the model sees `Bash npm test` and the bank
records the same line whether it passed or failed.

### B8 — major — injected-memory stripping is tag-dependent, and one emitted tag is never reconciled — `context-blocks.ts:29-42`

The regex requires a literal closing tag with a backreference `</\1>`. It covers both of the plugin's
tags plus the four `hindsight_*` spellings — but the plugin only ever emits `wenyuan_memory`
(`index.ts:222,242`). `KNOWLEDGE_BLOCK_TAG` (`:22`) is declared and **never used**; the comment at
`:21` ("attached when the bank's knowledge pages could be read") describes a feature that does not
exist (§3.2). Two concrete holes: a block whose body contains an unclosed `</wenyuan_memory>` string
truncates the strip mid-block, and the session-start block is greeting-shaped, so a model that
quotes it in its reply re-emits it into the write-back. Memory therefore can still eat its own
output — the exact failure the module exists to prevent (`transcript.ts:13-15`).
Note the source is already `source.kind: 'wenyuan-memory'` (`source.ts:14`), which the projection
could use to drop injected messages wholesale instead of regex-stripping text.

### B9 — major — tool failures are surfaced as *successful* tool results — `index.ts:388-392, 458, 474, 497, 536, 570, 591, 604, 625, 639`

`textTool.execute` returns `toolValue(await run(...))` and every failure branch returns a *string*
(`return 'WenYuan knowledge search failed: …'`), so the pipeline records `isError: false`
(`dsh-tools/lib/pipeline.js:205`, `dsh-agent-loop:961` commits it as a success). Upstream is explicit
about why that is wrong: *"The tool bodies return MCP-shaped results and never throw; dsh wants a
value or a throw… if `result.isError` throw"* (`dsh.ts:345-357`). A model that sees a plain text
result cannot tell "the bank is empty" from "the service is down"; `wenyuan_diagnose` in particular
reports `WenYuan diagnose failed: …` as a successful diagnosis.

Probed (fetch rejects): `wenyuan_search_knowledge_pages` → `{"text":"WenYuan knowledge search failed:
ECONNREFUSED"}`, i.e. success.

### B10 — major — the reported "configured" credential can be the *other* reference — `index.ts:159, 271-281`

`resolveApiKey` returns the first of `WENYUAN_API_KEY`, `WENYUAN_API_TOKEN`, but the card reports
only `WENYUAN_API_KEY` (`API_KEY_CARD_REF`, `:162`; `settings-controller.ts:191`). On a deployment
that exports `WENYUAN_API_TOKEN` (the documented fallback), the card says "no key configured" while
every request is authenticated — and there is no way to see or clear the token that is actually in
use. `credentials.resolve` is real: refs are `^[A-Za-z_][A-Za-z0-9_]*$` and both names fit
(`dsh-credentials/lib/index.js:13`), so both resolve.

### B11 — minor — per-call credential read and client allocation — `index.ts:188-194, 433`

`clientNow` resolves credentials on every call, and tools call it per invocation. Correct for
rotation, but each `wenyuan_list_knowledge_pages` costs a credential-store read plus a fresh object.
A per-agent memo invalidated on `credentials/reference-updated` keeps rotation and drops the cost;
upstream caches a `RuntimeCore` per workspace (`dsh.ts:170-200`).

### B12 — minor — hygiene — `index.ts:321-326, 364-392`

* `writeBack(agent, ready, clientNow, perSession, signal)`: five positional seams whose two
  `clientNow` signatures (`:188`, `:324`) must stay structurally identical by hand.
* `textTool`'s `render` defends with `?? ''` (`:384`), masking a schema violation that
  `ToolOutputError` (`dsh-tools/contracts.d.ts:284`) would otherwise surface.

### B13 — minor — the projection registration is double-cast, hiding a peer mismatch — `index.ts:176-177`

```ts
176:  const transcriptReady: Promise<{ stateOf(session: Session): readonly TranscriptTurn[] }> =
177:    ctx.sessionProjections.register(wenYuanTranscriptProjection) as unknown as Promise<…>
```

In `0.2.0-rc.2` `register()` returns `() => void`; the `Promise<ProjectionHandle>` form is
`0.2.2-alpha.2` (`dsh-session-projection/lib/types/index.d.ts:186-198`). `init` also differs
(`(header)` vs `(header, inheritedEventCount)`). Meanwhile `peerDependencies` is
`^0.2.0-rc.2`, which does **not** admit the running `0.2.2-alpha.2` (`semver.satisfies` → false).
The `as unknown as` is what lets both typecheck, so the version window is unowned — nothing breaks
today only because the cast suppresses the check. (Verified separately: `createUserMessage` accepts
`{kind:'wenyuan-memory', form:'recall'}`, so `source.ts:19-23` is sound against alpha.2.)

### B14 — minor — the card hard-codes `overridden: false` and can hide a stored field — `settings-controller.ts:166-175`

`projection()` forces `apiKey: { overridden: false, invalid: false }` (`:175`). The primitives
define `overridden` as "the user layer carries this field"
(`dsh-client-ui-primitives/lib/index.js:5996-6000,6106-6118`), and `resetField` plans a write only
when `stored(field)` is true (`:6210-6219`) — so the `overriddenLabel`/`resetLabel` copy is dead for
section fields and the secret never shows a state change until the next `describe()`.
(`scope.subscribe(() => void this.readCredential())`, `:131`, is safe only because the inner
`try/catch` at `:191-195` is load-bearing by convention, not type.)

---

## 3. Behaviour vs upstream `dsh.ts` — what upstream does that this plugin does not

| # | Upstream capability | Upstream evidence | Wenyuan plugin |
|---|---|---|---|
| 1 | **Git-history seeding** — cold repo's bank seeded from `git log` on session start, kept current on every start | `session-start.ts:1-25` (`gitHeadSha`, `gitLogIsCurrent`, `commitsSince`), `ensureSeeded` (`dsh.ts:203-206`) | absent — no `node:child_process`, no git anywhere in `src/` |
| 2 | **Knowledge page roster / preamble** — "N knowledge pages cover this repository… call search" guidance, re-injected on a cadence, names never listed | `knowledge-injection.ts:20-60` (`indexLine`, `TOOL_GUIDE`) | `KNOWLEDGE_BLOCK_TAG` declared (`context-blocks.ts:22`) but **unused**; session start performs a *recall* instead (`index.ts:220`) |
| 3 | **Page sync** — pages rebuilt/refreshed after consolidation on a trigger | `knowledge-tools.ts`, `config.ts` (`refresh_cron`, `refresh_after_consolidation`) | tools read pages only; `wenyuan_capture_initiative` writes a *memory*, not a page, and says so (`index.ts:538`) |
| 4 | **Reflect** in the auto path (`autoInject: 'reflect'`, deprecated `autoReflect`) | `config.ts:135,579`; `autoReflect` honoured | `wenyuan_reflect` exists as a **tool only**; no automatic reflect |
| 5 | **Auto-update** of the installed package | `maybeAutoUpdate` (`session-start.ts:23`) | absent |
| 6 | **Codebase survey** — headless survey on start, `SURVEY_DOC_IDS` baseline | `startCodebaseSurvey`, `config.ts:209` | absent |
| 7 | **Initiative capture wired into session start** (in-flight initiatives in the preamble) | `knowledge-injection.ts:39-40` | `wenyuan_capture_initiative` is a tool a model *may* call; nothing prompts it beyond the description |
| 8 | **Multi-repo** — bank + core + seed per `session.header.cwd`, cached | `dsh.ts:10-15,155-161,170-200,372-376` | **B1: one bank per process** |
| 9 | **Disable switches** — global `disabled`, `optInOnly`, per-bank opt-out, `HINDSIGHT_DISABLE_HOOKS` | `config.ts:89,95`; `workspaceFor` caches `null` | only `injectContext`/`retainOnStop` (`index.ts:143-144`); no per-bank or env escape hatch |
| 10 | **Config file layering** — `~/.hindsight/coding-agent.json`, `banks.<id>` overrides, per-harness section, env | `config.ts:293,321-357,615-663` | four schema fields in `cordis.patch.yml`; no file, no per-repo override |
| 11 | **Session-start is `emit`, fire-and-forget** — the Web UI must not wait on a network round trip | `dsh.ts:198-201` | binds **`agent/created`**, which is `serial` and awaited before creation resolves (`runtime-types.d.ts:246-252`) |
| 12 | **`pre-step` registered `{prepend:true}`** so its injection reads last | `dsh.ts:396` | registered without options (`index.ts:226`) — order depends on load order |
| 13 | **Re-injection guard** — identical block not appended twice | `lastInjected` (`dsh.ts:256,276-281`) | none; see B5 |
| 14 | **`agent/disposed` cleanup** | `disposed` (`dsh.ts:292-296`) | none (a `WeakMap` keyed by `Agent` makes this mostly moot, but the projection handle is never disposed either) |
| 15 | **`promptOf` filters to `source.kind === 'user'`** — other plugins' context is not a recall query | `dsh.ts:222-230` | `promptTextOf` joins **every** text block in `messages` (`index.ts:284-292`), so another plugin's injected context becomes the recall query |
| 16 | **Diagnostics** (`diag(HARNESS, 'inject_ok', …)`, `log.debug('tools registered')`) | `dsh.ts:266-281,387-393` | nothing — a silently empty injection is indistinguishable from a healthy one |
| 17 | **Provider-agnostic**: imports nothing from dsh, structural typing | `dsh.ts:17-26`, `toDshParameters` | imports 8 `@deepseek-ai/*` peers at runtime (`package.json`); breaks when any of them shifts (B13) |

One point in the plugin's favour: the projection fold (`transcript-projection.ts`) is better than
upstream's `snapshotEvents()` re-read (`dsh.ts:186-190`) — incremental and host-only — it just needs
the compaction rules B7 fixes.

---

## 4. Cross-package duplication: is the README's "double-recall / double-retain" claim true?

**The claim itself holds.** `lifecycle.ts:80-84` declares no DSH entry (`HOST_LIFECYCLES` has only
`claude-code`, `codex`, `codebuddy`) and `hostLifecycle('dsh')` **throws** (`:91-97`); `dsh.ts:71-73`
sets `configFiles: []`. Even if a hook file *were* written, `hooks/stop.mjs` retains from the Stop
payload and `hooks/pre-prompt.mjs` recalls on its own state file (`src/runtime/hook-state.ts`) —
neither shares the plugin's cursor, so two unshared cursors over one conversation ⇒ two retains per
turn and two recalls per prompt. The claim is accurate.

**But it is unenforced.** `HOST_LIFECYCLES` is a data table; a future `dsh` entry would be
advertised by `declaredHostIds()` (`:100`) and get hooks *and* a plugin. The guard is a comment.

**Overlap that *is* live today** (all verified):

| Overlap | Evidence | Consequence |
|---|---|---|
| **Skill copied, but it describes a different integration** | `dsh.ts:98` → `copyPackagedSkill('dsh', home)` → `~/.dsh/skills` (`skill-locations.ts:35`); `SKILL.md:8-11` says "a lifecycle hook captures the session", `:20-23` tells the model to "call the `retain` tool", `:48-49` calls it "the same tool surface the installed MCP server exposes" | No `retain` tool exists in a DSH session (the plugin's is `wenyuan_ingest_document`), and the MCP server exposes **one** tool, `wenyuan_recall` (`runtime/mcp-server.ts:29,115`). The skill's instructions dead-end for a path documented as working. |
| **MCP exposes `wenyuan_recall`; plugin exposes no recall tool** | `mcp-server.ts:29-33`; `index.ts:259-261` registers eight tools, none a recall | Not a double-write (MCP only recalls), but a second recall surface with **different** bank resolution (MCP reads `~/.dsh/mcp.json` env; the plugin reads the settings section) and no shared credentials. |
| **Two memory front doors** | `dsh.ts:86-91` registers `~/.dsh/mcp.json`; `index.ts:260` registers in-process tools | Two tool vocabularies, two config sources. `README.md:41-43` already frames MCP as opting in to "the server's own tool surface" — so `install dsh` should skip it by default, or the server should gain the write tools that make it a real alternative. |
| **Skill refresh exists only on the hook path** | `session-start-stage.ts:3`; no skill code in `index.ts` | DSH inherits a skill nothing refreshes after an upgrade, because the hook that would refresh it is deliberately not installed. |

Net: **no double-recall / double-retain is reachable today**; the real duplication is the skill and
the MCP entry, and both are *inconsistent* rather than doubled.

---

## 5. Prioritized fix list

### P0

1. **Resolve the bank per session workspace** — `index.ts:170,191`. Read
   `agent.session.header.cwd`, map it to a bank (repo name, or the configured `bankId` as a prefix),
   cache by root the way upstream caches `Workspace` (`dsh.ts:160-178`). Tools must rebind to
   `exec.agent`'s workspace (`index.ts:433`), not to the process's.
2. **Make every seam unconditionally fail-open** — `index.ts:216-256`. Wrap each listener body in
   `try { … } catch { return next()/undefined }`; keep `agent/created`'s host call (`agent.inject`)
   inside the guard. Add a probe test (the suite has none) that mounts with `apiUrl: {}`,
   `credentials.resolve` throwing, and `agent.inject` throwing, and asserts creation still resolves.
3. **Bound the whole request, not just `fetch`** — `client.ts:330-358`. Wrap `request` in one
   `Promise.race` against the deadline (or reuse the abort signal on `response.text()`); today a
   slow body hangs `turn-stopping` for the transport default (B2).
4. **Return tool failures as errors** — `index.ts:372-392`. Give `textTool` a failure channel that
   throws (or returns `{isError:true}` through the pipeline) for every `!outcome.ok` branch
   (`:458,474,497,536,570,591,604,625,639`), matching `dsh.ts:345-357`.

### P1

5. **Advance the write-back cursor before the await, roll back on failure** — `index.ts:331,351`
   (B6).
6. **Teach the fold about surface replacement and tool results** — `transcript-projection.ts:86-102`:
   honour `event.surfaceOp.op === 'replace'` by dropping turns whose source seqs were shadowed
   (needs seq bookkeeping in the state), and read `tool/result` to stamp the action line with its
   outcome (`types.d.ts:382-396`).
7. **Do not recall on non-user messages** — `index.ts:284-292`. Filter to `source.kind === 'user'`
   (upstream `promptOf`, `dsh.ts:222-230`); a plugin's context block is not a question.
8. **Only mark the turn recalled on success, and de-duplicate identical blocks** — `index.ts:229-243`.
   Move the assignment after a successful inject; keep the last injected text per agent and skip an
   unchanged block (upstream `lastInjected`, `dsh.ts:276-281`).
9. **Skip session-start on `source: 'resume'`** — `index.ts:216-217`. Check the `source` argument
   (`runtime-types.d.ts:254`) so reopening a session does not re-recall and re-inject (B5).
10. **Report both key references on the card** — `index.ts:159,162`, `settings-controller.ts:191`.
    Describe `WENYUAN_API_KEY` **and** `WENYUAN_API_TOKEN` and say which one is in force.
11. **Strip injected memory by source, not by tag** — `transcript-projection.ts:58-77` and
    `transcript.ts:99-115`. Drop any message whose `source.kind === 'wenyuan-memory'`
    (`source.ts:14`) before rendering; keep the regex only as a fallback for foreign spellings.
    Delete or implement `KNOWLEDGE_BLOCK_TAG` (`context-blocks.ts:22`).
12. **Name the peer window honestly** — `package.json:57-73,84-99` and `index.ts:177`. Either pin
    `0.2.2-alpha.2` and remove the `as unknown as` double cast, or widen the range and handle both
    `register()` shapes at runtime. Right now the cast hides an incompatible pair (B13).

### P2

13. **Register `pre-step` with `{prepend:true}`** — `index.ts:226` — so the memory block reads last
    regardless of load order (`dsh.ts:396`).
14. **Add the missing diagnostics** — mirror upstream `inject_ok`/`inject_empty`/`tools registered`
    at `index.ts:222,239,260`; today an empty recall and a dead service look the same.
15. **Make the MCP/skill story consistent** — `dsh.ts:86-98`, `skill/SKILL.md:20-49`,
    `runtime/mcp-server.ts:115`. Either drop the DSH MCP registration (the plugin is the intended
    surface), or extend the packaged MCP server past the single `wenyuan_recall` tool so the skill's
    `retain`/correction instructions resolve. Regenerate the DSH skill copy from a template that
    describes the plugin's actual tools.
16. **Guard the lifecycle table against a future DSH entry** — `lifecycle.ts:64-84` — add a test
    that `dsh` has no lifecycle *and* that `dsh.ts:locations.configFiles` is empty, so the comment
    becomes a contract.
17. **Simplify the write-back signature** — `index.ts:321-326` — take an options object; memoize the
    per-agent credential read (B11).
18. **Test the seams, not just the names** — `tests/plugin.spec.ts:86` asserts the listener list;
    none of the six bugs above would have been caught. Each P0/P1 item deserves one failing-first
    test.