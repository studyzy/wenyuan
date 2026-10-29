# 06 — Runtime review: `integrations/coding-agents` (runtime, hooks, usage log)

Scope reviewed (all of it read line by line): `src/runtime/*.ts` (18 files, 1 400 lines),
`hooks/*.ts` (4 entry points, 135 lines), `src/stats/usage-log.ts` (178 lines).
Reference: `/code/hindsight/hindsight-integrations/coding-agents` — `src/core/hook.ts`,
`session-start.ts`, `retain-hook.ts`, `chat.ts`, `knowledge-injection.ts`, `usage.ts`,
`config.ts`, `src/mcp-server.ts`, `src/core/runtime.ts`.

**Process note.** `team_task_get` / `team_task_update` do not exist in this session's
toolset (`unknown tool "team_task_get"`), so task-1 could not be claimed or completed; nothing
outside this file was modified.

---

## 1. What each runtime module is

| Module | Lines | Job | Uses the network? |
|---|---|---|---|
| `layout.ts` | 37 | Per-user runtime dir `~/.wenyuan/<OWNERSHIP_MARKER>`; `mcpServerPath()` | no |
| `stage.ts` | 109 | Copies `dist/runtime` into the runtime dir wholesale (`rm` + `cp`), chmods 0755 | no |
| `deadline.ts` | 57 | Converts the host budget (`src/hosts/lifecycle.ts:42-43`) into a per-stage timeout; stop keeps a 5 s margin | no |
| `hook-contract.ts` | 136 | Disable guard (`WENYUAN_DISABLE_HOOKS`), `failOpen`, `readStdin`, payload parse + env fold, retry constants | no |
| `hook-host.ts` | 39 | Resolves which host fired: payload member → `WENYUAN_MCP_HARNESS` → **first declared host** | no |
| `hook-output.ts` | 54 | Claude-compatible envelopes (`hookSpecificOutput.additionalContext`, `systemMessage`) | no |
| `hook-state.ts` | 76 | Per-session JSON state (`injected`, `attempts`, `documentId`) under `~/.wenyuan/hook-state/<sha256(host\0session)>.json` | no |
| `hook-connection.ts` | 82 | Resolves `{apiUrl, bankId, apiToken}` from `~/.wenyuan/config.json` + `WENYUAN_API_*` env | no |
| `memory-client.ts` | 143 | Two calls only: `POST …/memories/recall {query}` and `POST …/memories {items:[{content}]}`; `textsFromRecall` | yes |
| `conversation.ts` | 98 | Session id + turn extraction from the payload; `conversationDocumentId = sha256(host\0session)`; prefix-based append detection | no |
| `session-start-stage.ts` | 76 | Skill refresh, then one recall with a fixed opener string, emitted as a context block | yes |
| `pre-prompt-stage.ts` | 94 | First-prompt-only recall of the raw prompt, up to 3 attempts, "once per session" flag in state | yes |
| `stop-stage.ts` | 71 | Reads turns out of the **Stop payload**, POSTs them as one new memory item every time | yes |
| `skill-refresh.ts` | 59 | Presence-gated re-copy of the packaged skill on drift | no |
| `server-presence.ts` | 43 | File test that the staged MCP server exists before install wires it | no |
| `mcp-server.ts` | 213 | Hand-rolled JSON-RPC: `initialize`, `ping`, `tools/list`, `tools/call` for one tool (`wenyuan_recall`) | yes |
| `hooks/*.ts` (4) | 135 | Thin adapters: guard → stdin → stage → envelope → exit 0; stop prints nothing; mcp entry ignores argv beyond `--transport` | — |
| `src/stats/usage-log.ts` | 178 | Reader + aggregator for `~/.wenyuan/usage.jsonl`; **no writer exists anywhere in the package** | no |

Schema paths do exist server-side for everything upstream uses (`clients/openapi.json`:
`/memories`, `/memories/recall`, `/reflect`, `/knowledge-base/search`, `/knowledge-base/pages`,
`/knowledge-base/tree`, `/documents`). The gap is the client, not the service.

---

## 2. Correctness bugs

Severity: **blocker** = memory is wrong, lost, or never produced; **major** = a documented behavior is silently broken; **minor** = hygiene.

### C1 — blocker — knowledge-page injection does not exist at all
Upstream fetches the page set every turn, matches locally, and re-injects the roster on a cadence:
```ts
// upstream core/hook.ts:353-355 (cadence, default pageRefreshEveryTurns = 1)
const stale = !cached.pages || (cadence > 0 && turns - cached.pages.atTurn >= cadence);
// upstream core/hook.ts:457-462
if (cadence > 0 && turns % cadence === 0) {
  blocks.push(buildRosterRefresh(pages, { reflectOnNewGoals: cfg.autoInject !== "reflect", ... }));
}
```
Wenyuan runtime: zero occurrences of `knowledge-base`, `pages`, or a roster; `MemoryClient`
(`src/runtime/memory-client.ts:59-78`) exposes exactly two paths, `/memories/recall` and
`/memories`. Every instruction to call `hindsight_search_knowledge_pages` is a dead reference.

### C2 — blocker — Stop never re-reads the transcript, so most turns are never stored
Upstream reads the host's transcript file and normalizes it:
`// upstream core/retain-hook.ts:122 const turns = readTranscript(transcriptPath);`
Wenyuan instead mines whatever the *Stop payload* happens to contain:
```ts
// src/runtime/conversation.ts:70-78
for (const key of ["turns", "messages", "conversation", "transcript"]) { ... }
const single: string | undefined = firstString(payload, ["prompt", "content", "text"]);
return single === undefined ? [] : [single];
```
Claude Code's `Stop` payload is `{session_id, transcript_path, cwd, hook_event_name,
stop_hook_active}` — no `prompt`, no `messages`. `hooks/stop.ts:29` passes it straight through,
so `conversation.turns` is empty and `stop-stage.ts:41-43` returns `{written:false}` **on every
Claude Code session**; `transcript_path` is read nowhere in the package.

### C3 — blocker — the written document has no stable identity and no dedup
The document id the stop stage computes is returned but never sent:
```ts
// src/runtime/stop-stage.ts:55-58
const outcome = await client
  .capture({ content })            // -> POST /memories {items:[{content}]}
  .catch(() => ({ ok: false as const }));
return { written: outcome.ok, documentId };
```
```ts
// src/runtime/memory-client.ts:76
const response = await this.post("/memories", { items: [item] });
```
The server accepts `document_id`, `update_mode` and `operation_id`/`metadata`
(`internal/memoryops/retain_models.go:21-36`, any other key is dropped), but none is sent, so
every firing mints a *new* document. `documentId` (`conversation.ts:22-24`, `sha256(host\0session)`) never reaches the request, and
`hook-state.ts`'s `documentId` is never consulted by `runStop`. Upstream upserts under
`conversation:<sessionId>` with a deterministic operation id:
`// upstream core/chat.ts:371 const refId = \`conversation:${sessionId}\`;`
`// upstream core/chat.ts:434 opId = uuidV5(...) ; submit(..., updateMode: append ? "append" : undefined, operationId, ...)`
A Stop firing twice (resume, retry, an errored turn) retains the same text twice, each copy
re-extracted into facts.

### C4 — blocker — every turn re-sends the whole conversation (no cursor, no append)
```ts
// src/runtime/stop-stage.ts:53-54
const previous: readonly string[] = options.previousTurns ?? [];
const content: string = composeConversation(previous, conversation.turns);
```
`previousTurns` is optional and **no caller supplies it** (`hooks/stop.ts:28-32` passes only
`hostId`, `payload`, `timeoutMs`), so `appendedTurns([], turns)` returns all turns
(`conversation.ts:55`) every time. There is no retain cursor: upstream keeps one per session
(`core/retain-cursor.ts`, `writeSession` at `core/chat.ts:281+`) and skips already-sent turns.
Even in principle "previous turns" could only come from `hook-state`, which no writer ever
fills with turns (`hook-state.ts:19-26` stores `injected`/`attempts`/`documentId` only).

### C5 — blocker — Stop's "deadline" can exceed the host's kill and never bounds the write
Interactive stages take the whole host budget; stop subtracts a margin **in the host's declared
unit, applied as ms**:
```ts
// src/runtime/deadline.ts:39-43
const marginMs: number =
  registration.budgetUnit === "seconds" ? STOP_DEADLINE_MARGIN_SECONDS * 1000 : STOP_DEADLINE_MARGIN_SECONDS;
return Math.max(MINIMUM_STOP_DEADLINE_MS, budgetMs - marginMs);
```
With a non-`seconds` budget this returns `budget - 5` (5 ms, not 5 s), and line 43 clamps a
misconfigured small budget to `1`, so the stage can be handed a timeout the host does not
honour. Two further budgets are uncoordinated: `hooks/stop.ts:31` passes this value as
`timeoutMs`, but `hooks/stop.ts:23` already spent unbounded time in `readStdin()`
(`hook-contract.ts:65-75`, no deadline), and the deadline clock starts *after* that read.
Upstream starts its clock at process start, before the stdin read (`core/retain-hook.ts:189`
`const hostDeadline = Date.now() + spec.hostTimeoutSec * 1000 - HOST_DEADLINE_MARGIN_MS;`).

### C6 — major — no retry, no 429 handling, no "maybe committed" state
`src/runtime/memory-client.ts:80-107` makes a single attempt: no `Retry-After`, no retry window
derived from stop's remaining budget, and abort / non-2xx / JSON failure all become the same
`{ok:false}` (lines 97-103), so a timeout after the server *accepted* the write is
indistinguishable from a rejected one. Upstream retries 429 inside the host deadline and keys
each payload by `operation_id` so a resend collapses (`core/chat.ts:205-240, 271-300`).

### C8 — major — retrieval query is the raw prompt and is neither bounded nor typed
```ts
// src/runtime/pre-prompt-stage.ts:76-78
const outcome = await client.recall(prompt)
// src/runtime/memory-client.ts:66
const response = await this.post("/memories/recall", { query });
```
Upstream bounds the query (500 tokens, `BYPASS` for separate calls) and sends
`recallOptions` (`types: ["world","experience"]`, a token budget, `prefer_observations` like
settings) — `core/hook.ts:139-155`, `core/config.ts:141-150`. Wenyuan sends only `{query}`,
so the default budget/type mix is whatever the server chose; long prompts hit the service's
query cap and the turn gets nothing.

### C7 — major — a kill mid-retain loses the turn irrecoverably
Seen from the operator's side (distinct from C4's resend): nothing in the runtime records *what* was written
(no cursor, no `pending` outbox), so a kill mid-retain — the normal case under C5 — leaves the
turn unrecoverable. Upstream's whole `pending` + replay design exists for exactly this
(`core/chat.ts:470-490`).

### C9 — major — failure and empty are flattened, and one timeout decides the session
`outcome.ok && outcome.texts.length > 0` (`pre-prompt-stage.ts:79`) puts *failed* and *empty* in
one branch, and the loop breaks on either at line 86-87; then every prompt after the first sees
`state.injected` false but also never retries, because nothing else writes state. Upstream keeps
them apart: failures retry on 2 turns, empties on 5, each budget per source, and a tri-state
(`null` = ran-and-empty, `undefined` = failed) so a first-prompt timeout is *not* cached as "this
session has no memory" (#4607, `core/hook.ts:113-131` `resolveInjection`).

### C10 — major — `attempts` is never incremented; the retry/empty budgets are dead code
```ts
// src/runtime/pre-prompt-stage.ts:53-58   (gate reads state.attempts)
if (state.attempts >= EMPTY_RESULT_BUDGET) { return ...; }
...
await writeSessionState(options.sessionKey, { ...state, injected: false, attempts: attempts });
```
`writeSessionState` is reached only at line 81 (success, local `attempts` still 0) and line 93
(local `attempts` starts at `state.attempts`, which is always 0, and is never incremented). So
the per-turn empty budget never decays and `RETRY_BUDGET` / `EMPTY_RESULT_BUDGET`
(`hook-contract.ts:35-39`) do not do what their doc comment says.

### C11 — major — no banner, no notice, no failure visibility
`session-start-stage.ts:41` sends a generic string; on a failure the session renders nothing.
Upstream puts the bank name and cold/warm state in `systemMessage` (`buildSeedBanner`,
`core/session-start.ts:94-102`) and emits a "no memory this turn" notice pointing at the diag
file (`core/hook.ts:497-501`). Wenyuan has no diagnostics file at all — there is nowhere an
operator can look to tell "memory had nothing to say" from "the hook is broken".

### C12 — major — `stop`'s write-back carries no context, tags or harness
`capture()` sends `{items:[{content}]}` with no `tags`, `context`, `metadata` or `source`
(`memory-client.ts:74-78`). Upstream sends `source:chat`, `harness:<id>`, `session_id`,
`ref_id` and a `context` that tells extraction whose claim a sentence is
(`DEFAULT_RETAIN_CONTEXT`, `core/chat.ts:254-261, 418-432`). Downstream extraction cannot
tell an agent's "fixed" from an established fact (#4791) and the documents carry no harness.

### C13 — major — session-less firings share one state file
`hook-state.ts:39-41` keys on `hostId\0sessionId`, and `pre-prompt.ts:29` falls back to
`"anonymous"` when the payload carries no session id. Every session without a session id
therefore shares **one** state file — the first such session's injection silences every later
one (`pre-prompt-stage.ts:52-55`).

### C14 — minor — the output envelope is not per-host
`hook-output.ts:14-22` emits Claude's `hookSpecificOutput` shape for every host; `hook-host.ts`
resolves the host but nothing picks the envelope by it. Latent: today's three hosts are all
Claude-compatible.

### C15 — minor — `stageRuntime` has a window with no runtime
A hook that fires between `rm` and the end of `cp` (another install, a parallel host session)
executes a missing/partial script; stage into a temp dir and `rename` over.

### C16 — minor — `server-presence.ts` hand-joins its path
`server-presence.ts:24-26` builds `${dir}/${file}` rather than using `join` — POSIX-only, though
`layout.ts:22-27` claims `USERPROFILE` support. Folded into fix 15.

### C17 — minor — stats reader mis-attributes
`src/stats/usage-log.ts:85` `turns: parsed["turn"] === false ? 0 : 1` counts one "turn" per
line, and line 87 `CREDIT_MEMBERS.some(...)` treats a *non-false value* rather than the upstream
credit rule. With no writer, the reader can never see a real line anyway (see §3.10).

### C18 — minor — tool failures are returned as successful text
`mcp-server.ts:130-136` returns `textResult(...)` (`isError` absent → the model reads it as a
*normative fact*), where a tool failure should be `{isError:true}`. Upstream guards this class of
failure at the protocol level (see §3.8).

### C19 — major — the MCP server ignores `WENYUAN_MCP_HARNESS`
`hooks/mcp-server.ts` never calls `resolveHostId`; `launchStdioServer` (`mcp-server.ts:199-213`)
never reads `WENYUAN_MCP_HARNESS`. The registration writes it (`src/mcp/ownership.ts:34`), and
the value is silently dropped. Upstream made it a required, throwing check precisely because a
wrong harness corrupts `harness:<id>` stamps and bank resolution
(`src/mcp-server.ts:53-74 resolveHarness`).

---

## 3. Behavioral differences vs upstream (the numbered upstream features)

### 3.1 Recall / injection strategy
| | upstream `core/hook.ts` | wenyuan |
|---|---|---|
| Where | `UserPromptSubmit` per turn; source = `cfg.autoInject` (`reflect` default) | `UserPromptSubmit`; source = `recall` only |
| Once vs per-turn | once per session **for the synthesis**; page roster per-turn at cadence (default 1); tool-guide refresh at cadence | once per session for *everything* (`pre-prompt-stage.ts:52-55`), then forever silent |
| Failure vs empty | tri-state, budgets 2 failure / 5 empty turns (per turn, per source) | 3 in-call attempts, no per-turn retry (C9, C10) |
| Session-start | banner + knowledge preamble + seed/survey kickoff | one recall of a fixed opener string (`hooks/session-start.ts:32`) |
| Query | prompt bounded to 500 tokens + `recallOptions` | raw prompt (C8) |
| Notice | goal + preview, or "no memory this turn — see <diag>" | only the static `DEFAULT_MESSAGE` (`session-start-stage.ts:41`), never on failure |

### 3.2 retrieve-vs-reflect
Upstream default: `autoInject: "reflect"` → `client.reflect(buildReflectQuery(prompt), {budget:
"low", timeoutMs: cfg.reflectTimeoutMs (default 20 s)})` (`core/hook.ts:276-283`,
`core/config.ts:489`); on a timeout/5xx it falls back to **knowledge-page search then raw recall,
sharing one budget** (`reflectFallback`, `core/hook.ts:172-189`); a permanent 4xx resolves
immediately (`core/hook.ts:302-305`). Wenyuan: no reflect path anywhere (`grep -r reflect
src/runtime` → 0), so the flagship "root cause + exact values" synthesis never happens, and the
fallback chain is moot because there is no page search to fall back to.

### 3.3 MCP protocol version / handshake
Upstream uses `@modelcontextprotocol/sdk` `McpServer` + `StdioServerTransport`, which negotiates
the revision the client asked for and rejects an unknown one (`src/mcp-server.ts:16-17,138`).
Wenyuan hand-rolls JSON-RPC (`mcp-server.ts:94-142`). Correct: notifications (no `id`) are
always silently dropped (line 98-100), which MCP requires; `ping` is answered; unknown methods get
`-32601`. Wrong or missing: an *unsupported* `protocolVersion` is answered with the newest one
(line 104-107) instead of an `initialize` error; `initialize` is answered again after
`initialized`; `resources/*`/`prompts/*` get `-32601` rather than a capability-aware error; no
`tools.listChanged` is declared; `SERVER_NAME = "wenyuan-memory"` (line 23) is not AGENTS.md's
`wenyuan-mcp-server`.

### 3.4 Tool surface
Upstream assembles `buildKnowledgeTools` (`core/knowledge-tools.ts:136-333`):
`hindsight_sync_status`, `hindsight_diagnose`, `hindsight_search_knowledge_pages`,
`hindsight_list_knowledge_pages`, `hindsight_read_knowledge_page`, `hindsight_reflect`,
`hindsight_capture_initiative`, `hindsight_ingest_document` — with safety `annotations` per tool
(the Dcode/Codex headless reason, `src/mcp-server.ts:113-115`).
Wenyuan serves **one**: `wenyuan_recall(query)` (`mcp-server.ts:28-42`), no annotations.
The shipped skill (`skill/SKILL.md`) instructs the agent to call `retain`,
`create_knowledge_page`, `search_knowledge_base`, `get_knowledge_page`,
`get_knowledge_base_tree`, `reflect` — **none of which exist** on either server. The README's
"the same tool surface the installed MCP server exposes" is false in both directions.

### 3.5 Tool-guide text / knowledge preamble
Upstream SessionStart injects `buildKnowledgePreamble` (`<hindsight_knowledge>…` with the
first-stop search rule, the credit-blockquote rule, per-tool triggers, roster count)
`core/knowledge-injection.ts:119-133`; every `pageRefreshEveryTurns` turns it re-injects
`buildRosterRefresh` (`:141-150`). Wenyuan injects a bulleted list of recalled strings and
nothing else (`session-start-stage.ts:75-77` `- ${text}`); there is no tool guide, no credit rule,
no "search before you act" trigger anywhere in the runtime.

### 3.6 Session write-back document shape
| | upstream | wenyuan |
|---|---|---|
| Source | host transcript file, normalized turns (`chat.ts:99-121`) | payload keys, usually none (C2) then raw text |
| Content | JSONL, one `{"role","content","timestamp"}` per line (`chat.ts:467`) | `"\n"`-joined plain text (`stop-stage.ts:70-72`) |
| Increment | append the new turns via `update_mode:"append"` + `operation_id` | no append; resend everything (C4) |
| Identity | `conversation:<sessionId>` + `uuidV5(bank\nrefId\nmode\ncontent)` | none sent (C3) |
| Metadata | `source:chat`, `harness:<id>`, `session_id`, `ref_id`, `context` | none (C13) |

### 3.7 document_id / dedup
Upstream: `refId = conversation:<sessionId>` (`core/chat.ts:371`) plus a deterministic
`operationId` per payload (`core/chat.ts:417,434`), a persisted cursor with a `pending` outbox
and an append-capability probe (`writeSession`, `core/chat.ts:271-490`). Wenyuan:
`conversationDocumentId = sha256(host\0session)` (`conversation.ts:22-24`) computed, returned,
and thrown away (`stop-stage.ts:58`). No server-side upsert, so a repeated Stop is a duplicate
document; an append is impossible even after a fix, unless `update_mode` is used.

### 3.8 Fail-open
Both never throw to the host. Differences: wenyuan's `failOpen` wrapping is correct for
`session-start`/`pre-prompt` (`hooks/*.ts:29-41`) and stop (`hooks/stop.ts:27-33`); the MCP
server has no equivalent guard around `launchStdioServer` (`hooks/mcp-server.ts:13` — an
unhandled rejection at startup kills the host's server silently). Upstream's MCP path catches
and `process.exit(1)` with a stderr message (`src/mcp-server.ts:143-147`), and every hook is a
`try { } catch { }` (`core/session-start.ts:363-436`).

### 3.9 Timeouts and hook-timeout budget
Upstream: reflect 20 s automatic / 330 s tool, inject 7 s, all bounded by *each* host's
registration (60 s Claude/Codex, 30 s Cursor/Antigravity, `core/retain-hook.ts:57-61`), stop
margin 2 s (`HOST_DEADLINE_MARGIN_MS`, `core/retain-hook.ts:33`), page+recall fallback share one
budget (`reflectFallback`, `core/hook.ts:180-186`).
Wenyuan: interactive 10 s / stop 30 s (`src/hosts/lifecycle.ts:42-43`), margin 5 s in the
declared unit (`deadline.ts:39-43`), a single request per stage; the whole 3-attempt loop runs
under one `timeoutMs` per call, so `pre-prompt` can cost 3× the request timeout with no overall
deadline.

### 3.10 Usage / stats
Upstream `core/usage.ts` writes `~/.hindsight/coding-agents-logs/usage.jsonl`, one line per
finished user turn `{ts, harness, session, bank, turn, calls, credited}`, with a per-session
cursor, `reviseLastTurn` re-emit, rotation (`usage.jsonl.1`), and a report that dedupes by
`(harness, session, turn)`. Wenyuan's `stats` reads `~/.wenyuan/usage.jsonl`, whose only
possible writer is the retain hook — which no longer calls it. So `stats` is *permanently
empty*: `readUsageLog` → `[]`, `buildReport` → `{hosts:[], toolCounts:[]}`, `formatReport` prints
a bare header. The README admits "Nothing in this slice writes the usage log"; the code comment
`usage-log.ts:7` says the same. This is a whole feature, not a slice gap.

---

## 4. Prioritized fix list
### P0 — memory is wrong or absent today
1. **`src/runtime/conversation.ts:70`** — read the host transcript named by `transcript_path`
   (Claude/Codex JSONL) before falling back to payload keys, so Stop has turns at all (C2).
2. **`src/runtime/stop-stage.ts:55`** — send `document_id` (`conversation:<sessionId>`),
   `update_mode:"append"` (first write `"replace"`) and a deterministic `operation_id` from the
   payload bytes, so a repeated Stop upserts instead of duplicating (C3, C4, C5).
3. **`src/runtime/stop-stage.ts:53`** — thread a real per-session retain cursor (turn count +
   fingerprint + pending outbox, as upstream `retain-cursor.ts` does) in from `hooks/stop.ts:28`
   (C4, C7).
4. **`src/runtime/deadline.ts:39`** — `stop` must return `budgetMs - 2000` and the clock must
   start before `readStdin()` in `hooks/stop.ts:23`, so the host never kills the write (C5).
5. **`src/runtime/memory-client.ts:59`** — add `searchKnowledgePages`/`listPages`
   (`GET …/knowledge-base/search`, `…/tree`), and in **`src/runtime/pre-prompt-stage.ts:76`**
   make page search the path (with raw recall the fallback), as `reflectFallback` does (C1).
6. **`src/runtime/memory-client.ts:80`** — retry 429/non-2xx inside the stage deadline using
   `Retry-After`, and distinguish "maybe committed" from "rejected" (C6).

### P1 — parity that changes what the agent sees
7. **`src/runtime/pre-prompt-stage.ts:56`** — implement the tri-state (`failed` vs `empty`) with
   separate 2/5-turn budgets and persist `attempts` on every path (C9, C10).
8. **`src/runtime/pre-prompt-stage.ts:76`** — bound the recall query (byte-cost cap ≈500 tokens)
   and send `types`/`max_tokens` beside `query` (C8).
9. **`src/runtime/session-start-stage.ts:63`** — replace the fixed-opener recall with upstream's
   session-start shape: bank banner in `systemMessage` (`buildSeedBanner`) plus a
   `buildKnowledgePreamble`-style tool guide and roster in `additionalContext` (C11, §3.5).
10. **`src/runtime/mcp-server.ts:32`** — grow the tool surface to `search_knowledge_pages`,
    `list_knowledge_pages`, `read_knowledge_page`, `reflect`, `capture_initiative`,
    `ingest_document` (with `annotations`), and make **`skill/SKILL.md`** name only real tools (§3.4).
11. **`hooks/mcp-server.ts:13`** — resolve the host from `WENYUAN_MCP_HARNESS` and refuse to
    start without it, as upstream `resolveHarness` does (C19).
12. **`src/stats/usage-log.ts:91`** — add the writer (`core/usage.ts` equivalent) called from
    `hooks/stop.ts:28`, with a per-session cursor and `(harness, session, turn)` dedupe (§3.10).

### P2 — hygiene and robustness
13. **`src/runtime/mcp-server.ts:130`** — return `{isError:true, content:[…]}` for unreachable /
    unknown-tool / empty-query failures (C18).
14. **`src/runtime/hook-state.ts:39`** — key on `sessionId` alone, and skip the stage when there
    is none instead of aliasing every such firing to `"anonymous"` (C13).
15. **`src/runtime/stage.ts:99`** — stage into a temp dir and `rename` over; same pass,
    **`src/runtime/server-presence.ts:25`** uses `path.join` (C15, C16).
16. **`src/runtime/hook-output.ts:34`** — pick the envelope from `resolveHostId`'s host (C14).
17. **`hooks/mcp-server.ts:13`** — wrap `launchStdioServer` in `.catch` → stderr + exit non-zero,
    as upstream `src/mcp-server.ts:143-147` does (§3.8).
18. **`src/runtime/mcp-server.ts:104`** — echo a requested `protocolVersion` only when known,
    else an `initialize` error listing supported revisions; answer `resources/*`/`prompts/*` with
    capability-aware errors (§3.3).

---

### Assumptions / non-verified (stated so they are not mistaken for evidence)
- That the Claude Code Stop payload carries no `prompt`/`messages` is the documented host
  contract, not executed here; either way `conversation.ts:70` gets no usable turns.
- The server's keep-default when only `document_id`/`update_mode` are sent was inferred from
  `internal/memoryops/retain_models.go` field presence, not from a live round-trip.
