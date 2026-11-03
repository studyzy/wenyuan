# Migration contracts — shared runtime port (internal working notes, not shipped)

Rules for every contributor (human or agent):

1. Clean-room naming. Nothing in this package may carry the reference module's
   name (a leakage scanner enforces the token). Say "the reference module".
   Rename table: the reference tool names -> wenyuan_* (same eight tools);
   WENYUAN_* env keys -> WENYUAN_AGENTS_*; user agent
   wenyuan-coding-agents/<version>; uuid namespace const WENYUAN_NAMESPACE with
   a freshly generated fixed value (not the reference's); config file
   ~/.wenyuan/config.json (existing layout; see src/config/connection.ts).
2. Style: TypeScript ESM, 2-space indent, English comments, JSDoc explaining
   WHY not WHAT (match existing src/ tone). Code files < 500 lines, tests
   < 800. Tests live under tests/unit/** (vitest, offline, no network; mock
   fetch/globalThis as needed).
3. Deliberate deviation: the reference's local daemon mode is cut. Do not port
   DEFAULT_DAEMON_PORT / DEFAULT_DAEMON_PROFILE or any serverMode "daemon"
   branch; the client always points at a wenyuan URL.
4. Do NOT modify existing src/{cli,commands,config,hosts,mcp,runtime,stats,
   write}/** in this wave, and do not edit package.json / tsconfig / build
   config (the coordinator owns those). New code goes to src/core/** only.
5. Bank URL shape: cross-check src/runtime/memory-client.ts and keep ITS route
   shape (already validated against the served API).

## Wave 1 module ownership + pinned cross-module signatures

### Agent "core-foundations" owns (port behavior from the reference module's
### src/core/<name>.ts; read the matching upstream test for pinned behaviors):

- src/core/util.ts      exports: binOnPath, sleep, semverGte, pool, isOurMcpEntry
- src/core/uuid.ts      exports: WENYUAN_NAMESPACE, uuidV5(name, namespace?)
- src/core/jsonl.ts     exports: readJsonl generator (+ writeJsonl if the
                        reference has it)
- src/core/log.ts       exports: logsDir, LOG_MAX_BYTES, appendLogLine,
                        LogLevel, setLogLevel, logFilePath, log object,
                        describeError
- src/core/diag.ts      exports: diagFilePath, diag, parseHookEvent, readHookEvent
- src/core/types.ts     exports: ChatSession, ChatReader, HarnessAdapter
- src/core/brand.ts     exports: brand constants (wenyuan spellings)
- src/core/attribution.ts (as the reference, renamed)
- src/core/config.ts    exports: RawConfig, Config, resolveConfig,
                        readEnvConfig, loadConfig, applyBankConfig, AutoInject,
                        DEFAULT_REFLECT_TIMEOUT_MS (20_000),
                        DEFAULT_REFLECT_TOOL_TIMEOUT_MS (330_000),
                        DEFAULT_INJECT_TIMEOUT_MS (7_000). env keys renamed per
                        rule 1. Load precedence/env names: port exactly.
- src/core/bank.ts      exports: BankConfig, ProjectRoot, BankResolutionError,
                        resolveProjectRoot, projectNameOf, isOptedIn,
                        pathSection, deriveBankId, bankProjectName,
                        deriveBankIdOrSkip
- src/core/host-client.ts exports: HostMemory, resolveHostConfig,
                        resolveHostMemory
- tests: tests/unit/core/<area>.test.ts per area above.

### Agent "api-client" owns:

- src/core/retain-stamp.ts (small, pure)
- src/core/missions.ts   (769-line pure-logic module: retain strategies,
                        knowledge-page taxonomy, page triggers. Tool names
                        inside prompts/answers become wenyuan_*.)
- src/core/client/       the HTTP client, split to stay under 500 lines/file:
                        index.ts (class MemoryClient composing the parts),
                        http.ts (transport: signed fetch, 401 re-resolve +
                        single replay, 429 backoff with Retry-After floor 10s /
                        ceiling 60s / jitter, RateLimitedError, hard per-request
                        AbortSignal timeout, default 15s), retain.ts (retain
                        always-async + op id collection, drain poll: 5s cycle,
                        give up after 12 consecutive 5xx, listDocumentIds,
                        idempotent/append capability probes via /v1/version
                        then /version, min version 0.8.6), knowledge.ts (page
                        search/list/read/create, tree, shapePage collapsing
                        body/markdown, DEFAULT_PAGE_SEARCH_LIMIT 10,
                        endpoint-missing 404 probe semantics: only the API's own
                        JSON {"detail":"Not Found"} latches "unsupported"), 
                        reflect.ts (ReflectError with fallbackEligible /
                        permanent). DEFAULT_RECALL_OPTIONS {types:["observation"],
                        budget:"low",max_tokens:2000,include:{entities:null}}.
                        observation_scopes sent on EVERY retain; per_source
                        resolved client-side to [[], [source], ...] sorted.
- tests: tests/unit/core/missions.test.ts, retain-stamp.test.ts,
                        client.test.ts (mocked fetch; pin retry/backoff, 401
                        replay, capability probes, drain give-up, shapePage).
- May import ONLY from: util, log, diag, uuid, config types, retain-stamp,
  missions, package.json version. Report precisely if something else is needed.

### Agent "transcript-generic" owns:

- src/core/transcript-util.ts, src/core/transcript.ts (generic reader),
  tests/unit/core/transcript.test.ts.
- Depends on jsonl + types per pinned signatures. The reference's generic
  transcript module is small (118 + 68 lines); port it fully. If it imports
  lifecycle modules that do not exist yet, port everything that compiles and
  list the deferred imports exactly in the report — do not stub or invent.

## Gates (every agent, before reporting)

cd /code/wenyuan/integrations/coding-agents
npx tsc -p tsconfig.json --noEmit   (or: npm run typecheck)
npx vitest run tests/unit/core      (scoped to your own test files first)

## Report format (keep under 40 lines)

files written; gate results with exact pass/fail counts; deviations from the
reference and why; deferred items (imports you could not satisfy yet).

## Wave 2/3 ownership (appended 2026-11-03, from the reference import graph)

The reference's lifecycle layer imports its background layer directly, so the
wave split follows the real coupling:

### Wave 2a "injection": inject.ts, knowledge-injection.ts, knowledge-tools.ts
(the last needs status.ts — pull status.ts's knowledge-page counters here; its
git/survey parts stay with 2b and are passed in as arguments, not imports, if
that keeps the split clean — otherwise status.ts belongs to 2b and 2a defines a
minimal counter interface).

### Wave 2b "chat-state": chat.ts, turn-journal.ts, retain-cursor.ts,
session-cache.ts, usage.ts.

### Wave 2c "background-small": git.ts, seed.ts, skill-dirs.ts, skill-sync.ts,
auto-update.ts, survey.ts, survey-lease.ts, status.ts (whole module; 2a then
takes the counter interface, not the file).

### Wave 2d "lifecycle apex" (starts only after 2a-2c land): hook.ts,
retain-hook.ts, session-start.ts, runtime.ts. Daemon branches are CUT
(deviation rule 3); where the reference passes a daemon client, the port
passes the ordinary URL client — one code path.

### Wave 3 "transcripts": zstd-frames.ts, the 17 per-host readers
(transcript-*.ts), history.ts (needs chat + readers + zstd-frames). Split
readers across agents by host family; every reader test pins a recorded shape
from the reference's reader tests.

Cross-wave rule: a module lands only when its own tests pass; agents never
modify modules owned by another agent — report the missing piece instead.

### Pinned by wave 1 (do not re-litigate)

- TransportTurn lives in src/core/transcript.ts (the producer), NOT types.ts.
  types.ts's ChatSession is the backfill abstraction (turns carry text);
  transcript.ts's TransportTurn is the reader shape. turn-journal / retain-hook
  / history ports import TransportTurn from transcript.ts.
- Injection block tags are wenyuan_* (wenyuan_memory / wenyuan_knowledge / ...);
  the strip set in transcript-util.ts names our tags. Neutral wrappers
  (hook_prompt, task-notification, system-reminder, relevant_memories,
  user_feedback) keep their names.
- Comments never carry the reference tracker's issue numbers; measured evidence
  numbers stay.
- tsconfig has noUncheckedIndexedAccess on — indexed access needs handling.

### Wave 2 dependency-cycle rule (pinned)

knowledge-tools must NOT import status.ts. It receives its status counters
through a narrow interface defined beside knowledge-tools; status.ts (wave 2c)
implements that interface. This breaks knowledge-tools -> status ->
knowledge-injection -> knowledge-tools.

### Wave 2 reconciliation duty (coordinator)

wave 2 launch requires: client re-exports ObservationScopes /
DEFAULT_OBSERVATION_SCOPES / DEFAULT_PAGE_SEARCH_LIMIT from ../config (single
source; no copies in client/constants.ts), and DEFAULT_SEED_LIMIT imported from
config by the seed port.

### Wave 4 obligations (accumulated from wave 2/3 reports — coordinator checklist)

- stage.ts: write .install-origin.json {source} + package.json {version} into the staged
  runtime (~/.wenyuan/wenyuan-memory); stage the skill/ directory too — without both,
  auto-update is dormant and staged skill-sync finds no packaged skill.
- Single skill path: core/skill-sync.ts is the session-start sync lifecycle; the old
  src/runtime/skill-refresh.ts + src/hosts/skills.ts copyPackagedSkill are retired or
  delegated — one path only.
- stats command points at core/usage.ts formatUsageReport; delete src/stats/usage-log.ts +
  USAGE_LOG_FILE_NAME.
- Generated SKILL.md must carry the recapture-on-plan-change contract (knowledge-tools test
  expects it; reference skill lines 37-39).
- engines is now >=22.15 and CI's coding-agents job runs Node 22 (zstd for dsh transcripts).
- deepen engine: git.ts defaults to a deepen.js beside the module — confirm ownership when
  the deepen port lands (wave 6); until then the spawn path fails soft.
- Deferred tests to add on landing: codex "counts the MCP call in summarizeTurns and
  recordUsage" (owner w3-history), the "every session-start path checks for updates"
  source-grep guard (owner wave 2d), git-stderr.test.ts (owner wave 2d).

### Wave 4 pinned conventions (read before writing any adapter)

- Staged runtime layout: ~/.wenyuan/wenyuan-memory/ carries dist/, skill/,
  package.json {version}, .install-origin.json {source}. Host registrations name
  the staged copy's bin by ABSOLUTE path.
- Per-host hook bins ship as dist/<host>-<event>.mjs where <host> is the host id
  and <event> in {hook, sessionstart, stop} (only the events that host defines;
  claude-code/codex keep their single dispatching <host>-hook.mjs). Each bin is
  a thin shim: read the host's event payload (stdin or argv per that host's
  dialect), call core hook.ts / retain-hook.ts / session-start.ts, write the
  output the host expects. NO business logic in a bin.
- build.config.mjs gains one entry per bin. package.json bins gain
  wenyuan-<host>-<event> entries (the installer never relies on PATH lookup —
  it writes absolute staged paths — the bins exist for npx/manual use).
- New hook hosts register through that host's OWN config dialect exactly as the
  reference does (per-prompt matcher groups vs flat entries vs TOML); the
  adapter owns the shape, the bin stays host-agnostic.

### Wave 4 decision log (coordinator, 2026-11-03)

- Host roster correction: the reference's factory-droid and droid are ONE host (Factory
  Droid; two bin spellings upstream). Nine unique new hosts: dcode, antigravity-cli,
  devin-cli, grok-build, qwen-code, zcode, traecode, factory-droid, kimi — registry lands
  at 16 hook hosts + 7 plugin hosts.
- Bin convention REVISED: this package keeps its shared staged bins
  (dist/runtime/hooks/{session-start,pre-prompt,stop}.mjs + mcp-server.mjs) with per-host
  registration entries and per-host payload shapes in hosts/hook-shape.ts — NOT the
  reference's per-host bin files. One dispatch path, existing verify machinery unchanged.
  The earlier per-host-bin pin is superseded.
- antigravity statusline: deferred (needs a build entry); tracked for a follow-up wave.
- git-stderr.test.ts: deferred until sync.ts + session-start.ts land (they did — owner:
  wave 2d follow-up).

### Coordinator decisions on W5 flags (2026-11-03)

- Budget tables: the harness hook-lifecycle table (30s interactive / 60s stop; reference
  values) is the single source. hosts/lifecycle.ts (installer side) must align to it in the
  installer wave; add a drift cross-check test then. A 10s installed budget would kill a
  20s reflect — do not ship it.
- Manifest: plugin exports are ./dsh ./opencode ./opencode2 ./kilo ./pi ./prime-agent
  ./cline pointing at dist/plugins/<name>.mjs (this package spells dist files .mjs); build
  gains one bundle per entry + a root plugins index + dist/runtime/hooks/deepen.mjs (seed
  default now deepen.mjs).
- kimi id mapping: harness roster says kimi-code, hosts id says kimi — installer wave pins
  the mapping (host id kimi, harness name kimi-code) and cross-checks it.


## Final state (2026-11-03, end of the port session)

All seven waves landed. Gates on the final tree, all green: tsc 0 errors;
build 16 entries; unit 99 files / 1439 passed / 3 skipped; e2e 25 files /
207 passed / 1 skipped; check:integration 58/58; leakage clean; engines
>=22.15 (CI on Node 22). This file is the migration's working record — keep
it beside the package until the residual list below is empty.

### Residuals (each small, listed so none hides)

- integrations/dsh-plugin: DELETED (2026-11-03, git rm -r). Its behavior is
  absorbed into src/plugins/dsh.ts + hosts/dsh.ts; the only surviving mention
  is the migration hint hosts/dsh.ts prints for machines still carrying the
  old profile row (@studyzy/dsh-wenyuan-memory) — that row is removed with
  one dsh plugin remove on those machines.
- check:integration recall-depth covers the claude reader family
  (claude-code, cursor-cli); every other hook host asserts lifecycle-ran.
  Per-reader recall behavior is pinned by each transcript reader's unit suite.
- survey-supervisor's detached e2e passes outside vitest but is SIGKILLed by
  the dev sandbox under the runner; it is env-guarded
  (WENYUAN_AGENTS_SUPERVISOR_E2E=1) for CI or a dev machine.
- antigravity statusline shim deferred (needs a build entry when wanted).
- Wave-7 per-host Docker e2e cases: the runner (src/e2e) and stub-model are
  landed; per-host declarative setups are one entry each in
  src/e2e/harnesses.ts as machines with those CLIs come online.
