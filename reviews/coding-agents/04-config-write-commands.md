# 04 — Config model, write primitives, commands/CLI, skill generation

Scope read in full: `src/config/{connection,paths,store}.ts`, `src/write/{jsonc-edit,jsonc-scan,
json-merge,backup,marker}.ts`, `src/commands/*.ts`, `src/cli/{argv,main,usage,log}.ts`,
`scripts/generate-skill.mjs`, `skill-src/preamble.md`, `skill/SKILL.md`,
`src/runtime/skill-refresh.ts`, plus the call sites they feed (`src/hosts/{hooks,hooks-remove,
skills,select,registry}.ts`, `src/mcp/{json-server-map,toml-table}.ts`,
`src/runtime/hook-connection.ts`, `src/hosts/skill-tools.ts`).
Reference: upstream `src/core/config.ts`, `src/installer.ts` (§`configureServer`, §`run`),
`src/core/{daemon,bank,auto-update}.ts`, `scripts/build-skill.mjs`, preamble, `skill/SKILL.md`,
README §"Where memory lives" / §"Configuration" / §"Reference".

**Process note.** `team_task_get` / `team_task_update` do not exist in this session's toolset, so
task-3 could not be claimed or completed. Only this file was written: probe files were scratch
vitest cases, since deleted; a rebuild of the git-ignored `dist/` was reverted file by file;
`/tmp` is cleaned up. **[PROBED]** marks findings reproduced by running the real module under
vitest or the built CLI rather than inferred from reading.

---

## 1. Config model comparison

Wenyuan persists exactly five keys (`connection.ts:19-25`, "exactly five and no more") into
`~/.wenyuan/config.json`: `serverMode, apiUrl, apiToken, bankId, apiPort`. Upstream persists
~50 fields (`core/config.ts:41-301`) in `~/.hindsight/coding-agent.json`, layered env → file top
level → `harnesses.<name>` → `paths.<prefix>` → `banks.<id>`.

Wenyuan's installer/runtime symmetry is genuine, and better than upstream's accidental one:
`connection.ts:4-13` promises "the same file the runtime reads", and `hook-connection.ts:57` does
read it through the same resolver. Upstream's `installer.ts:937-938` has to *remember* to honour
`HINDSIGHT_CONFIG` on every read and write; wenyuan has one resolver. The gap is not plumbing, it
is everything the file is allowed to say.

### 1.1 Missing settings that change behaviour

| Upstream field (config.ts) | Default | Why it matters | Wenyuan |
|---|---|---|---|
| `bankIdTemplate`/`dynamicBankId` (L94-100; `bank.ts:53` `"coding-agent::{gitProject}"`) | dynamic | **Most damaging gap.** Upstream's default is one bank **per repo**; static `bankId` is the explicit opt-out. Wenyuan has no dynamic path: `DEFAULT_BANK_ID="default"` (`connection.ts:40`) is written literally (`:198,:207`). Every repo, agent and machine share one bank — recall returns another project's decisions, retain pollutes it. | missing |
| `mapPathToBank` (L93; `bank.ts:9-12`) | — | Longest-prefix path→bank; "overrides everything". Only way to give a monorepo subtree or client repo its own bank without touching a flag. | missing |
| `optInOnly`+`optInPaths` (L94-103; `bank.ts:255-269`) | false/— | Privacy control: memory runs **only** where approved; an unlisted project is inert (no bank created, nothing retained). Also the per-repo off-switch on a shared machine. | missing |
| `banks.<resolvedId>` (L287-295; `config.ts:906-925`) | — | Per-repo override of any behaviour, incl. `disabled` and a one-hop `bank` rename. The only practical per-repo control once a static bank exists. | missing |
| `paths.<prefix>` (L296-301) / `harnesses.<name>` (L280-282) | — | Per-directory (client dir → other tenant/token) and per-agent bank override, applied before `banks`. Four hosts are wired (`registry.ts:20`); the file should be able to split them. | missing |
| `retainSessions` (L110-115) | true | Whole-transcript write-back kill switch independent of recall and tools. Today the only way to stop transcript capture is `WENYUAN_DISABLE_HOOKS` (`hook-contract.ts`), which also kills recall. | missing |
| `disabled` (L108) | false | Hard off-switch for a no-memory baseline; rides the bank-resolution gate upstream, which wenyuan lacks. | missing |
| `retainTags`/`retainMetadata`/`retainContext` (L236-262) | —/—/built-in | Provenance: the only way a **shared** bank tells repos apart; `retainContext` is what extraction reads to attribute a claim to agent vs user. `MemoryClient` sends none. | missing |
| `manageBankConfig`+`retainExtractionMode`+`defaultBankConfig` (L263-334) | true/"concise"/— | What is written *into* the bank config (retain strategies, `knowledge` entity-label group, missions). Without it a fresh bank has no strategy for `git`/`conversation`/`document` retains; the server warns and extracts with its defaults. | missing |
| `pages`/`customPages` (L170-205) | all five/— | The seeded knowledge pages are what most sessions actually read; without them `search_knowledge_base` searches an empty set. | missing |
| `gitIngest`+`autoSeed`/`seedLimit`/`codebaseSurvey` (L206-223) | "message", true/300/true | Cold-repo seeding. A new repo starts empty and only conversations ever fill it. | missing |
| `autoInject`/`recallOptions`/`pageSearchLimit` (L116-160) | "reflect" | What session start injects. Wenyuan hard-codes one recall of a fixed opener (`session-start-stage.ts:32`) with no "none"/"pages"/shape knob. | missing |
| `autoUpdate` (L216-218; `core/auto-update.ts`) | true | Keeps the staged runtime current; without it a machine sits on an old runtime indefinitely. | missing (§4.6) |
| `maxParallelRetains`,`injectTimeoutMs`,`reflectTimeoutMs`,`pageRefreshEveryTurns`,`logLevel`,`toolGuideExtra` | — | Tuning/support knobs (429s have no `maxParallelRetains`; no sanctioned way to add local guidance). | missing |
| env fallback (`config.ts:836-905`) | — | One `HINDSIGHT_<FIELD>` per scalar, as a **fallback**. Wenyuan: 4 (`connection.ts:28-33`). | 4 of ~45 |

If only three were added: (1) `bankIdTemplate`/`dynamicBankId`, (2) `optInOnly`+`optInPaths`,
(3) `retainSessions` — the first two are correctness-and-privacy defects today; the third is the
only way to stop a transcript capture the user did not ask for.

### 1.2 Field-level divergences

- **`apiPort` is dead outside `local`**: `connection.ts:228-231` writes it only for `local` and
  deletes it otherwise (`undefined` → key removed, `store.ts:26-31`). Matches upstream (`apiPort`
  is daemon-only) but `README.md:76` advertises it as a stored key without saying so.
- **`WENYUAN_API_PORT` is also the server's own listen-port variable** (`internal/config/config.go:15`;
  repo README §"Environment"). Export it for the server and the `local` client moves; export it
  for the client and the server moves. The defaults agree, so this bites only on a mismatch.
  **P2**: prefix the installer's variables or document the overlap.
- Vocabulary: `hosted`/`self-hosted`/`local` (`connection.ts:15`) vs upstream's
  `cloud`/`self-hosted`/`daemon`; document the mapping. **P2**.
- `apiToken` is never re-read after a rejected request (upstream does: `config.ts` L50-52, README
  "When a change takes effect"), so a mid-session rotation needs a restart. **P2**.

---

## 2. Precedence: docs vs code

Docs (`README.md:72-78`, `cli/usage.ts:40-43`, `connection.ts:8-12`) state: **flag → stored →
deployment env override → default**.

| Key | Actual | Verdict |
|---|---|---|
| `serverMode` | flag → stored → default (`connection.ts:205`); **no env key exists** | docs overclaim: there is no `WENYUAN_SERVER_MODE`, so "export only `WENYUAN_API_URL`" implies nothing |
| `apiUrl` | chain honoured (`:218`), and env is still consulted on the short-circuit path (`:187`) | matches |
| `apiToken` | `firstNonEmpty(flag) ?? stored ?? env` (`:206`); **[PROBED]** stored `tok1` beats exported `tok2` on re-run | matches |
| `bankId` | **no flag** (`ConnectionFlags` `:42-46`; `argv.ts:35-39`); short-circuit `:198` returns `storedBank ?? envBank ?? DEFAULT`. **[PROBED]** first install with `WENYUAN_BANK_ID=team-a` stores it; a later install with `team-b` still reports and wires `team-a` | **broken**: env is write-once after the first install, and no flag can change it |
| `apiPort` | no flag; stored wins (`:177`) | same write-once shape |

The comment at `connection.ts:176` ("Stored before environment, matching the fixed precedence
chain") is honest about the code; the README's word "override" is not — env is a **fallback**, and
only on the first run. Upstream states this precisely and means it: "Environment variables are a
**FALLBACK**: the file wins wherever it sets a value" (`config.ts:836-845`, README:555-575). Either
copy that framing and semantics, or add a `--bank` flag and let an explicit env win.

### 2.1 `local` mode starts nothing

`SERVER_MODES` includes `"local"` but `localModeWarnings` (`connection.ts:112-120`) only checks
whether `wenyuan` or `docker` is on `PATH`; nothing spawns, probes `/health`, or writes a unit.
Upstream's `daemon` mode does: `ensureDaemon` (`core/daemon.ts:278-320`) adopts a healthy server
on the port, else spawns a detached starter, called at SessionStart and Stop — its own docstring
names `#3524` (a daemon mode that never started one) as the bug it exists to prevent. **[PROBED]**
`install --server local` against an empty `$HOME` exits 0 with one advisory warning, and every
later session gets `ECONNREFUSED`. Implement the start path, or make `local` mean "a server you
run at 127.0.0.1" and say so. **P1**.

### 2.2 `DEFAULT_API_PORT` — correct

`DEFAULT_API_PORT = 18888` (`connection.ts:38`) matches the service's own default
(`internal/config/config.go:260`, AGENTS.md:302), and `runtimeEnvironment`/`hook-connection.ts:67`
use the same constant. Upstream's 9077 exists *because* its server default is 8888 and "a daemon
must never squat on it" (`config.ts:40-44`); here the ports agree by construction, so reusing it is
right. The cost: `local` silently depends on the user's server listening on the default port, and
`connection.ts:104` builds `"${directory}/${name}"` with a forward slash instead of `join`
(harmless on Windows, still wrong). `derivedApiUrl` uses `127.0.0.1` while the Go CLI's default is
`localhost` (`internal/cli/config.go:25`) — a needless second spelling, no behavioural difference.

### 2.3 Other precedence defects

- **`hosted` is not a mode.** It ignores `apiUrl` entirely (`:186-187,218`) and has no hosted
  default URL, so even with a token it stores `http://127.0.0.1:18888`: it is `self-hosted` plus
  a mandatory token. `--server hosted` with `WENYUAN_API_URL=http://mem.internal` and no token
  fails at `:209-211` — **[PROBED]** — with a message that names only the token. Upstream's
  `cloud` branch deletes `apiUrl` and relies on a built-in Cloud default (`installer.ts:1074`).
  Give `hosted` a `DEFAULT_HOSTED_API_URL` or drop it. **P1**.
- **Self-hosted refuses a missing URL only when `--server` is on the command line** (`:221`), so
  bare `install claude-code` silently stores loopback (`connection.ts:36` default is
  `self-hosted`), while `install --server self-hosted` fails. Upstream in the same situation
  defaults to Cloud *and says so* (`installer.ts:1058-1063`). Print the chosen default and its
  source. **P1**.
- **The bank never reaches DSH.** `install.ts:102` passes `environment` (with
  `WENYUAN_BANK_ID`) to every host, and `hosts/dsh.ts:54-70` hands it only to the MCP entry. The
  DSH plugin the installer installs reads `bankId` from its own profile settings
  (`integrations/dsh-plugin/src/settings.ts:20-40`, default `"default"`) and never reads
  `process.env`. So `WENYUAN_BANK_ID=team-a` wires every host to `team-a` **except DSH**, which
  silently writes to `default`. **P1** — cross-slice, found from this side.

### 2.4 Store and path defects

- `store.ts:23-34` is a **non-atomic, unlocked read-modify-write** through
  `json-merge.ts:305-312` (`writeFile` in place). Two concurrent installs interleave; a crash
  mid-write truncates the file, and the next read treats it as `{}` (`json-merge.ts:29-31,40-42`),
  silently losing `apiToken`. Temp file + `rename`, and a lock. **P1**.
- `readJsonFile` (`json-merge.ts:27-31`) maps **every** read error to "not existed", so
  `storedConfigExists` (`store.ts:37-40`) reports `false` for an unreadable file and install
  "reconfigures" something it cannot write. **P2**.
- `paths.ts:20-27` prefers `process.env.HOME` over `os.homedir()`; a host that launches hooks
  with a cleaned environment can resolve a different home than the installer did. Upstream uses
  `homedir()` only, which also honours `HOME`, so this is a divergence in *consistency*, not
  behaviour. **P3**.

---

## 3. `write/` primitives — are the hand-rolled versions correct?

Upstream uses `jsonc-parser` (`modify`/`applyEdits`) for comment-preserving edits and `smol-toml`
for TOML. Wenyuan hand-rolls a scanner (361 lines), a text editor (378), a document merge (312)
and a TOML block editor. The scanner's control flow is sound — strings, line/block comments,
trailing commas, escapes, adjacent-comma deletion, CRLF, tab indent and `\r` line starts are all
handled — and most formatting cases are right. The defects are specific:

| # | Finding | Sev | Location |
|---|---|---|---|
| W1 | **Trailing comma in a container → invalid `},]`.** Insert appends `,`+entry after the last element's `end` without noticing a trailing comma. `{"command":"other"},\n{"command":"wenyuan-memory/x"},\n]` no longer parses (`JSON.parse`: `Unexpected token ']'`). Claude's `settings.json` is edited as `jsonc` here (`hosts/claude-code.ts:38`), which is exactly the dialect that allows them. `jsonc-parser.modify` handles it. **[PROBED]** | **P0** | `jsonc-scan.ts:312-315`; `jsonc-edit.ts:110-116,227-236` |
| W2 | **Removal can delete a host comment or a member.** `previousSignificant` stops at a comment by design (`jsonc-scan.ts:59-63`), so `extendOverAdjacentComma` (`jsonc-edit.ts:330-340`) and `deleteMember` (`jsonc-scan.ts:338-348`) miss the comma beside a comment and take one from the far side. `{"a":[…], // note, "b":1}` after removal keeps `// note` but drops `"b"`; a trailing comment on a removed element leaves the comment dangling against `]`. **[PROBED]** | **P1** | `jsonc-scan.ts:59-63,338-348`; `jsonc-edit.ts:330-340` |
| W3 | **A BOM disables every JSONC edit silently.** `rootObjectRange` checks `text[start] !== "{"` where `start = skipInsignificant(text,0)`; `\uFEFF` is neither whitespace nor `/` (`isWhitespace` `:24-26`), so it returns `null` and the caller "skips the host rather than rewriting a file it does not understand" — while still printing "wired". `asJsonDocument` (`json-merge.ts:36-44`) has the same problem via `JSON.parse`. A file saved by a Windows editor or PowerShell loses hook installation with no error. **[PROBED]** | **P1** | `jsonc-scan.ts:157-163`; `json-merge.ts:36-44` |
| W4 | **Non-object root is reported as "unparseable".** `[1]`, `null`, `42` all return `null` (`jsonc-scan.ts:157`) and surface as the generic "could not be parsed" message (`hooks-remove.ts:53-60`) — true but not actionable. Same fix as W3. **P2** | | `jsonc-scan.ts:157-163` |
| W5 | **Nested-insert indentation is wrong.** The hollow-container branch pads with `closingPad(object)+indent` (`jsonc-scan.ts:317-319`) and `buildMissingMember`/`buildNestedMember` restart depth at 0 (`jsonc-scan.ts:281-304`, `jsonc-edit.ts:360-378`). A new `"c"` beside a tab-indented `"b"` at depth 2 comes out one to two levels short. Valid, ugly. **[PROBED]** | P2 | as cited |
| W6 | **`"hooks": null` / `"hooks": "x"` reads as a parse failure, not "missing".** `jsonc-edit.ts:105,206-208` return `null` where the container is not the expected shape, and the caller reports "could not be parsed". **P2** | | `jsonc-edit.ts:105,206-208` |
| W7 | **Duplicate keys split-brain.** `findMember` takes the *first* (`jsonc-scan.ts:204-210`) while `JSON.parse` keeps the *last*; editing the first leaves the host reading the second. Nothing warns. **P2** | | `jsonc-scan.ts:204-210` |
| W8 | **Plain-JSON hosts are reserialized, losing comments.** `mcp/json-server-map.ts:105-127` writes `` JSON.stringify(document,null,2) `` for every JSON host (codex `settings.json` is `format:"json"`, `hosts/codex.ts:31`; DSH/codebuddy MCP maps too). So README:15's "Every write into a host-owned file is non-destructive" overclaims. **P1** | | `mcp/json-server-map.ts:105-127` |
| W9 | **Backup is non-atomic and its name invites the wrong reading.** `copyFile` has no fsync/rename, so a crash leaves a partial `.bak` that the existence guard then never repairs; and `uninstall` also calls it (`hooks-remove.ts:45`, `json-server-map.ts:117`), so the file to restore after uninstall is the state before the *first* install. Semantics are documented (`backup.ts:4-8`) but the suffix is not self-describing. **P2** | | `write/backup.ts:29-39` |
| W10 | `writeJsonFile` uses 0600 (`json-merge.ts:308-311`) while host writes are plain `writeFile` 0600 — no data-loss risk, no leak. **P3** | | `json-merge.ts:305-312` |

**Verdict.** The scanner is a competent re-implementation that handles *more* formatting cases than
a naive one, but it is not correct for two cases that make a host file unparseable or destroy a
host member (W1, W2) and one that silently disables the whole path (W3). Because upstream already
depends on the well-tested `jsonc-parser` for exactly this job, the cheapest correct fix is to
adopt it (or port `applyEdits`) rather than patch W1/W2/W5 by hand — each hand patch adds another
case the next author must reason about. None of W1–W7 is covered by
`tests/unit/jsonc-edit.test.ts` (8.7 KB); every probe above passed the existing suite.

---

## 4. `commands/` + `cli/` — argv, usage, exit codes, idempotency

**Exit-code contract.** `cli/main.ts:10-19`: no command → usage, exit **0**; unknown command →
usage, non-zero; every documented failure → non-zero. `main.ts:22-30` implements exactly that, and
`src/index.ts` is the library entry, not a CLI. The built dispatcher is `dist/cli/main.mjs`
(`package.json:10`) fed by `main.ts:60-63` — consistent, and self-execution is guarded by
`import.meta.url === pathToFileURL(process.argv[1]).href` (`:60-62`), which is the right test for
the in-process e2e suite.

Defects:

- **Args after `update`/`stats`/`uninstall` are silently dropped.** `parseArgs` collects `hosts`
  for every command but `main.ts:34-37` passes none: `wenyuan-agents update claude-code`
  re-stages the runtime, touches no host, exits 0. `usage.ts:25-26` says `update` "takes no host
  argument" but nothing refuses one. **P1**.
- **Unknown flags are silently dropped** (`argv.ts:80-86`) and `argv.test.ts:90-94` *pins* that as
  correct. `install --serevr local` succeeds; `--api-url --enable-workspace-mcp` swallows the next
  flag as the URL (`argv.ts:72-77`). For a tool that rewrites host configs a typo'd flag should
  fail. **P1**.
- **`--import-conversations` is a guaranteed no-op**: `install.ts:126-129` calls
  `attemptPastSessionBackfill` (`:141-150`), which always returns "no transcript importer is
  available in this package yet". Documented in `usage.ts:36` and README:69 as a real capability.
  **P1**.
- **`stats` can never report**: no writer for `~/.wenyuan/usage.jsonl` exists anywhere
  (`stats/usage-log.ts:7-13`; grep finds no writer). `stats.ts:17-20` says "no recorded usage yet"
  honestly, but README:122-130 lists no writer as a "known gap" only. **P1**: wire one, or say so
  in usage text.
- **The token is written twice in plain text**: into `config.json` (0600) and into every host's MCP
  entry as `--env WENYUAN_API_TOKEN=…` (observed in the printed `claude mcp add`), which also
  exposes it in the process table. **P2**.
- **Usage text accuracy** (`usage.ts:20-27`): does not mention that `install` also **stages the
  runtime** into `~/.wenyuan/wenyuan-memory` (`install.ts:46-54`), nor `--server`'s default, nor
  that self-hosted refuses without a URL. **P2**.
- `--enable-workspace-mcp` is wired correctly (`argv.ts:42-45` → `install.ts:108` →
  `claude-code.ts:118-129`) and refuses without the flag, with a copy-pasteable remedy. Good.
- Ordering is right: validation → staging → presence check → preflight → connection → wiring, and
  a failed connection leaves the stored config untouched (`install.ts:10-20,85-94`).

**Idempotency** (end-to-end against a fake home): a second `install claude-code` leaves
`~/.claude/settings.json` and the MCP entry byte-identical, and the `.bak` stable
(`backup.ts:33-36`). Three caveats: `stageRuntime` does `rm`+`cp` wholesale (`stage.ts:99-101`),
so a hook spawning in that window fails to load — upstream accepts the same window and documents
it (`auto-update.ts:30-35`), and `install` pays it on *every* run; the stored config is rewritten
(including deleting `apiUrl`/`apiPort`) on every run, so a file watcher fires each time; and a
second run is **not** idempotent when the environment changed — `--api-url` takes effect
(`:218`) while `WENYUAN_API_URL`/`WENYUAN_BANK_ID` do not (§2). **P1** for the last.

**Detection.** `detectHosts` = executable on `PATH` **or** the host's config directory exists
(`registry.ts:40-41`), so a leftover `~/.codex` makes `install all` wire Codex that is not
installed. Document it. **P3**.

**No update check.** `update` re-stages whatever `dist/runtime` the installed package carries
(`commands/update.ts:12-21`); there is no `autoUpdate` equivalent and no "a newer version exists"
notice. Upstream's `core/auto-update.ts` (453 lines) exists precisely because "a machine could sit
several versions behind indefinitely" (`:4-8`). At minimum `update` should report whether a
newer package version is published. **P2**.

---

## 5. Skill generation

**How it is produced.** `scripts/generate-skill.mjs` assembles `skill-src/preamble.md` (41 lines —
front matter and the four behaviour sections) followed by **one** marked README region,
`skill-usage` (`:37`, README:102-109), demoting headings (`:66-71`) and expanding `{{tool.*}}`
from a hard-coded map (`:24-31`). That map duplicates `src/hosts/skill-tools.ts:19-26`;
`skill-generation.test.ts:40-45` only checks the *file* names every identifier from
`skill-tools.ts`, so a divergence stays latent — import it instead. Upstream folds four large
regions (install, daemon settings, the whole Configuration section incl. the 45-row Reference
table, diagnostics) into a **730-line** skill, and gates it with `src/docs-freshness.test.ts`,
which also fails when a `RawConfig` field is named nowhere in the README (`build-skill.mjs:28-29`).
That tie between declarations and prose is what prevents the drift the script's docstring
describes (#3735).

**Is `--check` gated?** Indirectly: `package.json:29` defines `check:skill`, and
`skill-generation.test.ts:54-61` runs it, so the CI `npm test` step
(`.github/workflows/ci.yml:238-240`) does fail on a stale skill. That is a real gate but a weak
one: it compares bytes of a file generated from an 8-line README region and cannot notice product
drift (a new config key, host or tool). **P1**: add an explicit `check:skill` CI step and a
freshness test tying README flags ↔ `usage.ts` ↔ `argv.ts`.

**How the installed skill is refreshed.** `refreshInstalledSkill` (`skill-refresh.ts:28-52`) is
called from the session-start stage (`session-start-stage.ts:49-53`) with the resolved host
(`hooks/session-start.ts:25,34`). It is **presence-gated**: no installed `SKILL.md` → no refresh
(`:38-42`), so an uninstalled host never silently regains one. Good design. Problems:
(1) **DSH never refreshes** — it installs the plugin, not hooks (`hosts/dsh.ts:66-93`,
README:41-60), and `integrations/dsh-plugin` has no `SKILL.md` refresh, so a DSH user keeps the
first version until a manual re-install; upstream's plugin hosts read the skill from the staged
package at load. **P1**. (2) The refresh is awaited *before* the client opens
(`session-start-stage.ts:47-56`), on the hook's critical path with no deadline of its own. **P2**.
(3) It overwrites a user-edited skill; upstream's `toolGuideExtra` (`config.ts:145-151`) is the
sanctioned place for local guidance, and wenyuan has none. **P2**.

**Content depth** — 49 lines vs 730. Missing: the per-repo bank, what a knowledge page is, when
*not* to call `retain`/`reflect` (cost), a correction-doc example. Worse: `preamble.md:39-42`
mandates "a title prefixed by the correction marker" **without ever naming the marker**
(upstream's is `Correction: <topic>`, preamble L55-60), so the instruction is unexecutable as
written; and it says `retain` takes "the content and a short title", which reads as document
retain while the WenYuan route retains memories. **P1** for the skill's usefulness.

---

## 6. Prioritised fix list

### P0

1. `jsonc-scan.ts:312-315` / `jsonc-edit.ts:110-116,227-236` — trailing-comma insert emits `},]`;
   reuse the existing comma, and adopt `jsonc-parser` for the JSONC path.
2. `config/connection.ts:19-40,198,207` — no per-repo bank or path map; port
   `bankIdTemplate`/`dynamicBankId`/`mapPathToBank` from `core/bank.ts` (default
   `wenyuan::{gitProject}`, `"default"` only when there is no git).

### P1

3. `hosts/dsh.ts:54-70` + `integrations/dsh-plugin/src/{index,settings}.ts` — the installer's
   bank never reaches the DSH plugin.
4. `jsonc-scan.ts:59-63,338-348`; `jsonc-edit.ts:330-340` — comment-aware comma search.
5. `jsonc-scan.ts:157-163`; `json-merge.ts:36-44` — strip a leading BOM; report a non-object root
   distinctly.
6. `mcp/json-server-map.ts:105-127` — plain-JSON hosts lose comments; use the comment-preserving
   edit or refuse the rewrite.
7. `config/connection.ts:185-207`; `cli/argv.ts:35-39` — `bankId`/`apiPort` are write-once and have
   no flag; add `--bank` and either let an explicit env win or document "first run only".
8. `config/connection.ts:36,221` — print the non-interactive default and its source.
9. `config/connection.ts:186-187,209-211` — `hosted` ignores `apiUrl` and has no hosted default;
   give it one or remove the mode.
10. `config/connection.ts:112-120,214-216` — `local` starts nothing; implement a start path or
    redefine it.
11. `cli/main.ts:34-37`; `cli/argv.ts:72-86` — extra positionals and unknown flags are dropped
    silently; reject both.
12. `commands/install.ts:141-150`, `cli/usage.ts:36`, README:69 — `--import-conversations` is a
    no-op; remove until the importer ships.
13. `stats/usage-log.ts:7-13` — no writer exists; `stats` can never report.
14. `config/store.ts:23-34`; `json-merge.ts:305-312` — non-atomic unlocked config write holding
    `apiToken`; temp + rename + lock.
15. `skill-src/preamble.md:39-42` — never names the correction marker; add it, the per-repo-bank
    section and a correction example. Add an explicit `check:skill` step to
    `.github/workflows/ci.yml:238-240` and a freshness test tying declarations to prose.
16. `src/runtime/skill-refresh.ts` — unreachable for DSH; add an equivalent in the plugin.

### P2 / P3

17. `connection.ts:28-33` — `WENYUAN_API_PORT` collides with the service's listen port; prefix the
    installer's variables or document the overlap.
18. Per-host/per-directory layering (`paths`, `harnesses`, `banks`) is absent (§1.1).
19. `json-merge.ts:27-31` — every read error is "missing file"; distinguish `EACCES`/`EISDIR`.
20. `jsonc-scan.ts:281-320`, `jsonc-edit.ts:360-378` — nested-insert indentation (W5).
21. `jsonc-scan.ts:204-210` — duplicate keys split-brain (W7).
22. `write/backup.ts:29-39` — non-atomic copy; `.bak` semantics after uninstall (W9).
23. `cli/usage.ts:20-27` — add the runtime-staging side effect, `--server` default, non-interactive
    behaviour.
24. `commands/update.ts:12-21` — no staleness/version notice; document the no-op after upgrade.
25. `hosts/registry.ts:40-41` — detection via a leftover config directory.
26. `connection.ts:104` — `join()` instead of `"${dir}/${name}"`.
27. `connection.ts` — `apiToken` never re-read after a rejected request.
28. `scripts/generate-skill.mjs:24-31` — import the tool map from `hosts/skill-tools.ts`.