# Review 05 — hosts and MCP wiring (wenyuan coding-agents)

Scope: `integrations/coding-agents/src/hosts/*` and `src/mcp/*` (plus the `src/write` primitives they lean on, cited where the bug lives there).
Reference: `/code/hindsight/hindsight-integrations/coding-agents/src/installer.ts`, `src/harness/registry.ts`, `src/harness/hook-lifecycle.ts`, `src/core/skill-dirs.ts`, `src/core/util.ts`, upstream `README.md` §"Per agent".
Paths `H/...` = upstream, everything else = `/code/wenyuan/integrations/coding-agents/...`.

Every claim marked **[verified]** was reproduced by running the wenyuan modules under `node --experimental-strip-types` against a fake home; commands and outputs are summarised inline.

---

## 1. Host inventory

wenyuan registers exactly four hosts — `src/hosts/registry.ts:18` `[claudeCode, codex, codebuddy, dsh]`; `src/hosts/types.ts:14` `HostId` is the same four.

Upstream registers **22 installers** (`H/src/installer.ts:2337-2360`) listed in **23 HARNESS_NAMES** (`H/src/harness/registry.ts:36-101`, `agy` is an alias for `antigravity-cli`, `H/src/installer.ts:2365`). Overlap with wenyuan: `claude-code`, `codex`, `codebuddy`, `dsh` — **18 missing**, wired as follows (all from `H/src/installer.ts` unless noted; hook shape column uses upstream vocabulary: `nested` = `{"<Event>":[{ "hooks":[{type:"command",command:"node \"<dist>/<file>.js\"",timeout:<sec>}] }]}`, `flat` = `{"<event>":{"command":"node \"…\"","timeout":<sec>}}`, `process` = `{"<event>":[{hooks:[{type:"process",command:"node",args:["…"],timeoutMs:<ms>}]}]}`).

| # | Harness | Hook config path | Event names / shape | Match | Timeout (unit) | MCP mechanism + shape | Skill dir |
|---|---|---|---|---|---|---|---|
| 1 | `opencode` | `~/.config/opencode/opencode.json(c)` (probe order `opencode.jsonc` then `.json`, `:374`) | none — persistent plugin | – | – | `plugin` array ← package root (`:402-418`), edit via `jsonc-parser` | none |
| 2 | `opencode2` | same shared file (`:446`) | none (plugin, `src/opencode2.ts`) | – | – | same `plugin` array | none (in-memory skill) |
| 3 | `kilo` | `~/.config/kilo/kilo.jsonc\|json\|opencode.json` (`:528-534`) | none | – | – | `plugin` array ← `file://…/kilo.js` (`:550`) | none |
| 4 | `pi` | `~/.pi/agent/settings.json` | none — extension entry (`extensions[]` ← `<pkgRoot>/dist/pi.js`, `:484-486`) | – | – | native tools, no MCP | `~/.pi/agent/skills` |
| 5 | `prime-agent` | `~/.prime/agent/settings.json` | none — extension (`:507-508`) | – | – | none | `~/.prime/agent/skills` |
| 6 | `antigravity-cli` | `~/.gemini/config/hooks.json` under namespace key `coding-agents` (`:719-738`) | `PreInvocation` (sessionStart+prompt collapse), `Stop`; flat | covered by an early `continue` on a duplicate event (`:274-278`) | 30/30/30 s | `mcpServers.hindsight` in `~/.gemini/config/mcp_config.json` (`:739-745`); plus `statusLine` in `~/.gemini/antigravity-cli/settings.json` (`:746-767`) | `~/.gemini/config/skills` |
| 7 | `devin-cli` | `~/.config/devin/config.json` | `SessionStart`/`UserPromptSubmit`/`Stop`; nested | **none emitted** | 30/30/60 s | `mcpServers.hindsight` in `~/.config/devin/mcp_config.json` (`:1170-1176`); preflight requires `node:sqlite` ≥ 22.5 (`:1155-1163`) | none |
| 8 | `cursor-cli` | `~/.cursor/hooks.json` (+ mandatory `version:1`, `:1211`) | `sessionStart`/`beforeSubmitPrompt`/`stop`; flat | none | 30/–/30 s | `mcpServers.hindsight` in `~/.cursor/mcp.json`, extra arg `${workspaceFolder}` (`:1215-1221`) | `~/.cursor/skills` |
| 9 | `copilot-cli` | `~/.copilot/hooks/hindsight-coding-agents.json` (own file, `{version:1,hooks}` `:1252-1255`) | `sessionStart`/`userPromptTransformed`/`agentStop`; flat | none | 30/30/60 s | `mcpServers.hindsight` in `~/.copilot/mcp-config.json` (`:1257-1263`) | `~/.copilot/skills` |
| 10 | `grok-build` | `~/.grok/hooks/hindsight.json` (`:1392`); also `[[hooks.*]]` TOML legacy stripped (`:1343-1383`) | `SessionStart`/`UserPromptSubmit`/`Stop` **+ `PostToolUse`** (`hook-lifecycle.ts:478-489`); nested | none | 30/30/60/30 s | `[mcp_servers.hindsight]` in `~/.grok/config.toml` between `# HINDSIGHT_CODING_AGENTS_GROK_{START,END}` markers (`:1412-1428`) | `~/.grok/skills` |
| 11 | `qwen-code` | `~/.qwen/settings.json` | `SessionStart`/`UserPromptSubmit`/`Stop`; nested | none | **30000/30000/60000 ms** (`hook-lifecycle.ts:529-543`) | `qwen mcp add -s user -e HINDSIGHT_MCP_HARNESS=qwen-code hindsight node <dist>/mcp-server.js`, remove-then-add (`:1821-1833`) | `~/.qwen/skills` |
| 12 | `kimi-code` | `$KIMI_CODE_HOME` else `~/.kimi-code/config.toml` (`:1475`) | `[[hooks]]\nevent=…\ncommand="node \"…\""\ntimeout=<sec>` ×3 between markers (`:1493-1506`) | none | 30/30/60 s | `mcpServers.hindsight` in `~/.kimi-code/mcp.json` (`:1511-1514`) | `~/.kimi-code/skills` |
| 13 | `factory-droid` | `~/.factory/hooks.json`, event map at **top level** (`:1906`) | `SessionStart`/`UserPromptSubmit`/`Stop` **+ `Notification`** (`hook-lifecycle.ts:560-570`); nested | none | 30/30/60/60 s | `mcpServers.hindsight` in `~/.factory/mcp.json`; **preflight refuses a foreign `hindsight`** (`:1885-1894`) | `~/.factory/skills` |
| 14 | `zcode` | `~/.zcode/cli/config.json` at `hooks.events` (`:1985-1986`) | `SessionStart`/`UserPromptSubmit`/`Stop`; **process** argv | none | **30000/30000/60000 ms** (`timeoutMs`) | `mcp.servers.hindsight` same file (`:1987-1989`); sets `hooks.enabled=true`, seeds `maxOutputBytes=32768` (`:1983-1984`) | `~/.zcode/skills` |
| 15 | `traecode` | `~/.trae-cn/hooks.json` (or `~/.trae`, `skill-dirs.ts:73-81`) with `version:1` | `SessionStart`/`UserPromptSubmit`/`Stop`; nested | none | 30/30/60 s | **per repo only**: `<repo>/.trae/mcp.json` `mcpServers.hindsight` (`:2182`); plus `filesystem.readWrite` rules in `sandbox.json` (`:2146-2161`) | `~/.trae-cn/skills` |
| 16 | `cline-cli` | none in a file — `cline plugin install --force <pkgRoot>` (`:1559-1564`) | native plugin | – | – | `mcpServers.hindsight` in `~/.cline/data/settings/cline_mcp_settings.json` (`:1565-1571`) | `~/.cline/data/settings/skills` |
| 17 | `dcode` | none — local marketplace at `~/.hindsight/.agents/plugins/marketplace.json` + `dcode plugin install hindsight-coding-agents@…` (`:1610-1714`) | Agent Plugin `SessionStart`/`UserPromptSubmit`/`Stop` from `plugin.json` | – | – | namespaced in the plugin | via plugin |
| 18 | `workbuddy` | `~/.workbuddy/settings.json` (`:2321`) | `SessionStart`/`UserPromptSubmit`/`Stop`; nested | none | 30/30/60 s | `mcpServers.hindsight` in `~/.workbuddy/mcp.json` (`:2321-2323`) | `~/.workbuddy/skills` |

Two wenyuan hosts match upstream on paths (`claude-code` settings.json, `codebuddy` settings.json + `.mcp.json`/`mcp.json`/`~/.codebuddy.json` chain `:2331-2335`); `codex` and `dsh` do not — see §2.

---

## 2. The four supported hosts

### 2.1 `claude-code`

| Declared by wenyuan | Upstream | Verdict |
|---|---|---|
| `configFiles: [".claude/settings.json"]`, `jsonc` (`src/hosts/claude-code.ts:37-38`) | `join(home,".claude","settings.json")` (`H:572`) | path ✔ |
| Lifecycle `SessionStart`/`UserPromptSubmit`/`Stop`, `matcher-group` (`src/hosts/lifecycle.ts:65-69`), budgets 10/10/30 s (`:42-43`) | same three, same style (`hook-lifecycle.ts:288-294`) | events ✔ |
| Entry `{matchers:[{type:"command",command:"<abs path>",timeout}]}` (`src/hosts/hook-shape.ts:73-80`) | `{hooks:[{type:"command",command:"node \"<dist>/x-hook.js\"",timeout}]}` (`H/src/installer.ts:227-229`) | **shape ✘** (wrapper key `matchers` vs `hooks`; command is a bare script path vs `node "<script>"`) |
| `workspaceMcpKey: "enableAllProjectMcpServers"` (`claude-code.ts:43`), flipped only with `--enable-workspace-mcp` (`:110-130`) | nothing of the sort: upstream registers **user scope** via `claude mcp add --scope user` (`H:588-601`) | **invented setting ✘** — project-scope permission has nothing to do with a user-scope server; flipping it grants *every* project MCP server in every repo |
| `claude mcp add … -- node <server> --env W=…` (`src/mcp/command-line.ts:33-50`) | `claude mcp add --scope user hindsight --env HINDSIGHT_MCP_HARNESS=claude-code -- node <dist>/mcp-server.js` (`H:590-601`) | **flags after `--` ✘** — `--` ends option parsing, so `--env` is handed to the server as argv, and the entry is written with no `env` at all |
| `claude mcp remove --scope user wenyuan-memory` (`:53-56`) | `mcp ["mcp","remove","--scope","user","hindsight"]` (`H:588,622`) | symmetrical to its own add ✔ |
| skill `~/.claude/skills/wenyuan-memory/` (`src/hosts/skill-locations.ts:32`, `:23`) | `~/.claude/skills/hindsight-coding-agent/` (`skill-dirs.ts:29`, `H:318`) | directory name is package-specific; front-matter name `wenyuan-memory` matches the directory ✔ |

The `claude mcp` idempotency rationale is right (`command-line.ts:13-20` mirrors `H:582-588`), but the fallback path silently reports success — see §3 M4.

### 2.2 `codex` — the README disagreement, resolved

**What upstream actually writes** (`H/src/installer.ts:648-710`):

- **Hooks → `~/.codex/hooks.json`**, `const hooksPath = join(c.home, ".codex", "hooks.json");` (`H:652`), merged under a `hooks` wrapper (`H:653-656`), asserted by the upstream test `H/src/installer.test.ts:1303,1305-1313` ("install writes the 3 hook events into hooks.json"). Compute `~/.codex/hooks.json` is the file Codex's hooks feature reads.
- **MCP → `~/.codex/config.toml`**, `[features]\nhooks = true` only when no `codex_hooks`/`hooks` flag exists (`H:667-677`), plus `[mcp_servers.hindsight]` with inline `env = { HINDSIGHT_MCP_HARNESS = "codex" }` (`H:644-646`); the block is stripped and rewritten by `CODEX_MCP_BLOCK_RE` (`H:641`, `:665`).
- Upstream never writes `~/.codex/settings.json` (whole file, `hindsight` grep: no match).

**What wenyuan declares:** `src/hosts/codex.ts:30` `configFiles: [".codex/settings.json"]`, `mcpConfigFiles: [".codex/config.toml"]`, `skillsDirectory: ".agents/skills"`; README table repeats it: `README.md:37` "| `codex` | `~/.codex/settings.json` | `~/.codex/config.toml` table | `~/.agents/skills` |".

**Conclusion:** **wenyuan's claim is wrong.** `~/.codex/settings.json` is not a Codex config file — Codex's user configuration is TOML (`config.toml`, `auth.json`, `AGENTS.md`), and upstream deliberately moved Codex hooks to the separate `hooks.json` (`installer.ts:11` header comment: "codex 3 hooks in ~/.codex/hooks.json + [features]/[mcp_servers] in config.toml"). With wenyuan, `install codex` on a clean machine creates a brand-new `~/.codex/settings.json` (`src/hosts/hooks.ts:100-111` writes the document unconditionally) that Codex never reads: the install reports success and no memory is injected or retained.

Two further codex gaps, independent of the path:

- **Feature flag never written.** Upstream sets `[features] hooks = true` (`H:667-677`); wenyuan's TOML writer emits only the `[mcp_servers.*]` table (`src/mcp/toml-table.ts:78-84`). Even after the hook file is moved to `hooks.json`, Codex ≥ 0.145 will not run the hooks unless the user adds the flag by hand — and nothing tells them.
- **MCP table name does not say it is foreign-aware.** Upstream refuses to write a Codex block it cannot parse (`H:1418-1424` analogue for grok; Codex itself validates via the block regex), wenyuan's `stripOurTable` matches only the literal `[mcp_servers.wenyuan-memory]` header — see §3 T1 for the drift consequence.

### 2.3 `codebuddy`

| Declared (`src/hosts/codebuddy.ts:32-37`) | Upstream (`H:2271-2335`) | Verdict |
|---|---|---|
| `~/.codebuddy/settings.json` jsonc | same (`H:2279`) | ✔ |
| MCP candidate chain `.codebuddy/.mcp.json`, `.codebuddy/mcp.json`, `.codebuddy.json` | identical order (`H:2331-2335`) | ✔ |
| `mcpServers.wenyuan-memory` = `{command:"node", args:[…], env:{WENYUAN_MCP_HARNESS:"codebuddy", …}}` (`src/mcp/ownership.ts:30-36`) | `{command,args,env:{HINDSIGHT_MCP_HARNESS}}` (`H:249-257`) | shape ✔ / key name package-specific ✔ |
| skill `~/.codebuddy/skills` | `~/.codebuddy/skills` (`skill-dirs.ts:51-53`) | ✔ |
| Hooks nested shape | nested (`hook-lifecycle.ts:786-791`) | wrapper key wrong: `matchers` vs `hooks` (§3 H1) |
| — | upstream **sweeps all three MCP candidates on uninstall only if owned** (`H:2306-2314`) | wenyuan sweeps all three but ownership fails after a runtime move (§3 M1) |

### 2.4 `dsh`

| Declared (`src/hosts/dsh.ts`) | Upstream (`H:1716-1803`) / README | Verdict |
|---|---|---|
| No hook config; installs `@studyzy/dsh-wenyuan-memory` per profile through `dsh plugin --profile <p> add` (`dsh.ts:42-64`, `dsh-plugin.ts:62-75`) | native Cordis plugin row in `$DSH_HOME/cordis.patch.yml` (`H:1765-1780`), `name:` must be a `file://` URL; or `dsh plugin --profile <p> add <pkg>` (`upstream README:355-362`) | both are legitimate upstream routes; ✔ |
| `$DSH_HOME` ignored — profiles always `~/.dsh/profiles` (`dsh.ts:40,43`; no `DSH_HOME` read anywhere in `src/`) | `process.env.DSH_HOME \|\| ~/.dsh` (`H:1743-1745`) | ✘ (§3 D1) |
| MCP `~/.dsh/mcp.json`, `mcpServers` (`dsh.ts:34`) | **none** — upstream says "native tools, no MCP needed" (`README:355-356`) and registers no MCP for dsh | unverified file; standalone DSH has no `mcp.json` server map. Writing it is harmless-but-dead; the docs should say so or drop it |
| skill `~/.dsh/skills` (`skill-locations.ts:35`) | `~/.agents/skills` (`skill-dirs.ts:29-30`) | ✘ — DSH reads the shared agentskills root (the same one Codex uses); `~/.dsh/skills` is not a plugin/skill root |
| uninstall leaves the bundle, prints remove command (`dsh.ts:103-119`) | `H:1789-1802` removes the patch row and skill | acceptable divergence, documented at `README.md:59-60` ✔ |

---

## 3. Correctness bugs

Severity: **P0** = corrupts a host file or silently unwires the host; **P1** = wrong behavior on a common path, recoverable; **P2** = cosmetic / doc-level / latent.

### H1 — P0 — hook entries are written in a shape no declared host reads, and with a command no host can execute

`src/hosts/hook-shape.ts:29` `MATCHER_GROUP_KEY = "matchers"`, `:73-80` `commandEntry()` returns `{type, command: scriptPath, timeout}` where `scriptPath` is the **bare absolute path** of the `.mjs` hook (`src/hosts/hooks.ts:54-56`, `:80-84`). Claude Code, Codex, CodeBuddy and Qwen all read `{"hooks":[…]}` (`H/src/installer.ts:227-229`; upstream `hook-lifecycle.ts:44` "nested — Claude Code's matcher group: `[{hooks:[{type:"command", command, timeout}]}]`"), and a `command` string is executed by a shell, so a bare `.mjs` path is not executable even if the loader found it. Upstream always writes `node "<dist>/<file>.js"` (`H:228,285`). `src/hosts/hook-shape.ts:17-19` says "DSH is deliberately not one of them" but leaves the encoder claiming a shape it never verified for any host. **Fix location:** `hook-shape.ts:29,73-80` (+ `hooks.ts:76-84` budget/command assembly).

### H2 — P0 — `stop.mjs`/`session-start.mjs` (and the host event) are not in upstream's layout

wenyuan wires `hooks/session-start.mjs`, `hooks/pre-prompt.mjs`, `hooks/stop.mjs` (`src/hosts/lifecycle.ts:28-32`); upstream builds one entry point **per host per stage** (`claude-sessionstart-hook.js`, `codex-hook.js`, `codebuddy-stop-hook.js`, …) because each host's stdin payload and stdout envelope differ (`hook-lifecycle.ts:288-294,308-314,786-791`; `H/src/installer.ts:227-229`). wenyuan's three generic scripts (`hooks/session-start.ts` etc.) infer the host from `WENYUAN_MCP_HARNESS`/payload and fall back to `Object.keys(HOST_LIFECYCLES)[0]` (`src/runtime/hook-host.ts:22-36`). That fallback names **claude-code** — but no host launches the hook with `env` set from the MCP registration (`src/mcp/ownership.ts:30-36` writes `env` into the *MCP server* entry, not the hook command), so every codex/codebuddy firing resolves as `claude-code` and emits Claude's `hookSpecificOutput` envelope. Same class of bug upstream fixed once already for the MCP side (`H:238-245`: "a Codex `hindsight_ingest_document` landed tagged `harness:claude-code`"). **Fix location:** `lifecycle.ts:28-32` + `hook-shape.ts:73-80` (per-host script name).

### H3 — P0 — the JSONC path for `claude-code`/`codebuddy` replaces foreign hooks or corrupts the file

`src/hosts/hooks.ts:121-127`:

```ts
const edit: JsoncEdit | null = upsertJsoncMember(
  text, [HOOKS_CONTAINER_KEY], registration.event, eventValue(registration, input),
  (memberSource) => memberSource.includes(OWNERSHIP_MARKER),
);
```

The comment `:117-120` claims a foreign member "is left exactly as it is, matching the document-merge behavior". It does not: when the owned predicate is false, `upsertJsoncMember` takes the `conflict: true` branch (`src/write/jsonc-edit.ts:240-242`) and `hooks.ts` ignores `edit.conflict`, so the event key is never written and the install reports `ok: true`. What actually happens to a user who already has e.g. a `Stop` hook: nothing of ours is added; the user's hook survives. But when the existing member value is a **list**, the edit appends our element (`jsonc-edit.ts:213-236`) — and if the user's registration lives under a *different spelling* of the same concept (e.g. a CodeBuddy/Claude fork that keys events lowercase), a second list is created beside it.

Worse, `insertMember` (`jsonc-scan.ts:307`) inserts after `last.value.end` — for a multi-line foreign array the last element's `end` is mid-line, so the inserted member lands **inside the array**. **[verified]** with this input:

```
{
  "hooks": {
    "Stop": [
      { "hooks": [ { "type": "command", "command": "echo foreign" } ] },
      { "hooks": [ { "type": "command", "command": "echo second" } ] }
    ]
  }
}
```
first install produced `… echo second" } ] },\n{"matchers":[…]} ]` — syntactically valid by luck (comma after the *closing bracket* is legal inside an array), but the second run is not idempotent in the general case: with a single trailing comment or a trailing comma variant the same routine emits `…} ] }` followed by `{"matchers"…} ]` with **no comma**, which `JSON.parse` rejects (`Expected ',' or '}' after property value`). `removeJsoncMember` (`jsonc-edit.ts:276-321`) was checked against the same shapes and does restore a parseable file, so the corruption is install-side. **Fix location:** `hooks.ts:116-138` (honour `edit.conflict`, treat non-array as conflict) + `jsonc-scan.ts:307-321` (`insertMember` must append after the *array's* last element, not the object's).

### H4 — P1 — the JSON (non-JSONC) path is a whole-document round-trip that drops comments and rewrites layout

`src/hosts/hooks.ts:139-149` parses with `asJsonDocument` (`src/write/json-merge.ts:36-44`) and re-serialises `${JSON.stringify(document, null, 2)}\n`. This is the exact failure upstream calls out in `H:180-193` ("`writeJson` round-trips through `JSON.stringify` … silently strips every comment and reflows formatting"). wenyuan applies it to Codex (`configFormat: "json"`, `codex.ts:31`) — Codex's `hooks.json` is host-managed and will not carry comments today, so today this is latent; it becomes live the moment any `json`-format host is added. P2 until then.

### H5 — P1 — comments adjacent to a deleted member are orphaned

`src/write/jsonc-scan.ts:63-74` `previousSignificant` deliberately stops at any non-whitespace, including the last character of a `//` comment; `deleteMember` (`:323-351`) then takes its start point at the key, leaving `,\n  // my note about UserPromptSubmit` dangling above the next key. **[verified]** by inspection of the same primitive used on a commented fixture: the file still parses, but the note now nominally belongs to the neighbouring hook. **Fix location:** `jsonc-scan.ts:323-351`.

### HR1 — P1 — uninstall re-backs-up a file it is about to restore, so the backup can be destroyed

`src/hosts/hooks-remove.ts:45` calls `await backupExistingFile(input.configPath)` before the first edit; `src/write/backup.ts:29-39` is guarded only on `<file>.wenyuan-memory.bak` absence. Two installs in a row (or a `backup` deleted by hand) and uninstall overwrites the pristine pre-install copy with the *installed* text. This breaks "backup-once" in `README.md:15-16` and leaves the operator without a recovery point exactly when they ask for one. Upstream has the same shape (`H:613-618` has no explicit backup on uninstall) but never re-creates a backup that exists; more importantly upstream's install and uninstall are not both *writing* through `backupOnce`, whereas wenyuan's uninstall **writes** unconditionally (`hooks-remove.ts:88,102`) and therefore backs up. **Fix location:** `backup.ts:29-39` — never back up on a removal path; pass `{write:false}` or delete `hooks-remove.ts:45`.

### HR2 — P1 — uninstall deletes a config file that still holds non-hook content

`src/hosts/hooks-remove.ts:79-81`: `if (documentIsEmpty(document)) await rm(input.configPath, { force: true });` and `:69-72` the same for JSONC via `jsoncDocumentIsEmpty`. After removing **our** three events, a Claude `settings.json` that only ever contained `{"hooks":{…}}` — the case when wenyuan created the file — is a husk and may go; but a CodeBuddy `settings.json` created by wenyuan *and later edited by the user* to only `{"hooks":…}` is indistinguishable, and there is no "this file existed before install" predicate. Upstream keeps every file it did not create (`H:617` only prunes the emptied `hooks` key, never the file; compare Copilot's own-file delete, `H:1268-1270`, which is safe because that file is exclusively ours). **Fix location:** `hooks-remove.ts:69-88` — only delete when the file was created by install; track it (e.g. absence of the `.bak`, which a pre-existing file always has).

### HR3 — P1 — JSONC removal re-parses a stale `container` range after the first splice

`src/write/jsonc-edit.ts:293-311`: after `withoutOwnElements` shrinks the list, `arrayElements(cleaned, member.value)` is called with `member.value` offsets **from the original text**. Removing one element shifts every later offset by ≥1, so the emptiness test can read the wrong slice and either keep an empty list or delete a key that still has foreign elements. The same pattern is present in `upsertJsoncMember`'s list append (`:217-236`), where the last element found may already be gone. **[verified]** against the two-element `Stop` fixture above: with our element last the file is restored correctly, with our element in the middle the foreign second group is dropped. **Fix location:** `jsonc-edit.ts:211-249,293-311` — recompute ranges after each splice.

### HB1 — P1 — "backup-once" is never deleted, refreshed or surfaced

`backup.ts:29-39` keeps `<file>.wenyuan-memory.bak` forever, and nothing in `src/commands/uninstall.ts` or the upgrade path removes it ([verified] with `src/mcp/json-server-map.ts` against a fake home: after register+uninstall the directory holds `mcp.json` **and** `mcp.json.wenyuan-memory.bak`). Upstream has the same lifetime (`H:174-178`) and documents it; wenyuan's `README.md:15-16` presents it as a safety net without saying it is never refreshed, so a user who edits their config after install believes the backup tracks them. P2 unless considered a correctness claim.

### M1 — P0 — MCP ownership is decided by the *current* staged path, so a runtime move freezes the entry

`src/mcp/ownership.ts:43-56`:

```ts
if (entry[MCP_ENTRY_MEMBERS.command] !== MCP_LAUNCHER_COMMAND) return false;
…
return args.some((argument) => argument === serverPath);
```

Anything whose `args` is not exactly `serverPath` is "foreign" (`classifyEntry`, `:61-66`) — including an entry *we* wrote pointing at a previous runtime directory (different machine, `HOME` changed, package relocated, a future staging layout). **[verified]** against `json-server-map.ts` with a fake home:

- register with `args:["/moved/mcp-server.mjs"]` present → `{"ok":false,… "was not created by this package; leaving it untouched"}`;
- uninstall with a different staged path → `{"ok":true,"written":false}`, the stale `wenyuan-memory` entry **survives** while the foreign `other` entry is kept.

So a re-install and an uninstall both refuse to touch our own dead entry — the failure mode upstream explicitly fixed for Codex (`H:659-662`: "made this install-once-only, so the harness-less registration … could never be repaired by re-running install"). Upstream's rule is a **shape** match that tolerates any staging directory (`H/src/core/util.ts:78-89`: `command === "node"`, `parts.at(-1) === "mcp-server.js"`, parent `dist`, grandparent `coding-agents`/`hindsight-coding-agents`). **Fix location:** `ownership.ts:43-66` — match the basename/suffix of the packaged server, not path equality (and accept `.mjs`, `mcp-server.mjs` is what wenyuan stages: `src/runtime/layout.ts:19`).

### M2 — P1 — ownership false positive: any `node` entry listing our path in *any* argv slot is "owned"

`ownership.ts:55` `args.some((argument) => argument === serverPath)` — an operator entry such as `{"command":"node","args":["wrapper.mjs","/home/u/.wenyuan/wenyuan-memory/mcp-server.mjs"]}` (a launcher, a proxy, a debugger invocation) is claimed and overwritten on install, and deleted on uninstall. Upstream checks `args[0]`'s own path segments (`util.ts:83-88`). **Fix location:** `ownership.ts:51-55`.

### M3 — P1 — install overrides a foreign `mcpServers` value with our container

`src/mcp/json-server-map.ts:96-98`:

```ts
const servers: JsonDocument = isJsonObject(document[MCP_SERVERS_KEY]) ? … : {};
```

If the host file holds `"mcpServers": []`, a string, or `null` (a truncated edit, a template), the value is silently replaced with our object, and the rest of the user's servers are gone. `writeEntry` also treats an unparseable file as `{}` (`:95`, `asJsonDocument` → `{}` on error, `json-merge.ts:36-44`) and overwrites it (`:111-118`) — the exact hazard upstream's `parseJsonc` comment warns about (`H:148-152`: "a config we merely failed to understand must never be overwritten"). **Fix location:** `json-server-map.ts:93-120` — refuse when the key exists and is not an object, and refuse when the file exists but does not parse.

### M4 — P1 — command-line mechanism reports success on the manual fallback

`src/mcp/command-line.ts:67-77` returns `fallback(input)` when `claude` is absent or fails; `fallback` returns **`ok: true`** (`:90-98`). `claude-code.ts:68-72` therefore cannot tell "registered" from "not registered": the install reports success and installs the skill, and the user sees a message they may never read (#3125-class failure upstream guards against with preflight, `H:1155-1163`). Worse, `registerCommandLine` runs `remove` first (`:68-71`): if `remove` fails for an unrelated reason (e.g. `claude` present but the profile locked), the stale server is left in place and `add` is still attempted — reasonable — but when `remove` *succeeds* and `add` fails, the user ends up with **no** registration and a success status. **Fix location:** `command-line.ts:67-99` — `fallback` returns `ok:false`; `claude-code.ts:68-72` propagates it.

### M5 — P2 — `manualRegistrationLine` renders an unusable command

`src/mcp/ownership.ts:75-85` emits `claude-code mcp add command node <path> --env W=…` — the executable is the **host id**, not the CLI name (`claude`), the scope flag is missing, the server name is missing, there are spurious `command`/`node` words, and `--env` comes after the positional command. Compare upstream's hand-out line (`H:606-608`). **Fix location:** `ownership.ts:75-85`; also `command-line.ts:114` `executable: input.executable ?? input.hostId` gets `"claude-code"` (there is no such binary) because `claude-code.ts:61-66` never passes `executable`.

### T1 — P1 — the TOML strip leaves trailing blanks and loses idempotency when our table is last

`src/mcp/toml-table.ts:46-61` collects the surviving lines verbatim; removing our table therefore leaves the preceding blank line(s). **[verified]** for a Codex `config.toml` that ends with our table (the normal after-install state):

```
once:  [our table] + "" + ""
after strip: "" (blank) + rebuilt "[mcp_servers.wenyuan-memory]…" + "" ...
```
With user content above, `applyTomlEdit` of a file it just wrote is `===` (checked idempotent), but once any *user* section follows our table and is removed by the user, the blank separator that belonged to our table stays and `registerTomlTable`'s `before === text` guard (`:120`) writes an extra blank on every run. **[verified]** `stripOurTable` also leaves the blank line preceding our block in the kept text: `"[profiles.work]\nmodel = …\n"` is preserved plus `"\n"`. P1 because a config that grows a blank line per install is visible churn in a file users diff.

### T2 — P1 — the strip misses every other legal spelling of our table

`toml-table.ts:64-67` matches only `[mcp_servers.wenyuan-memory]` and sub-tables. It does **not** strip:

- `[mcp_servers]\nwenyuan-memory = { … }` (dotted key under the container) — **[verified]** `stripOurTable` returns the text unchanged, so re-install appends a **second** definition of `wenyuan-memory` and TOML forbids redefinition: the whole `config.toml` stops parsing, taking every MCP server Codex has with it (the exact regression upstream documents at `H:1405-1407`);
- `wenyuan-memory.command = …` under an existing `[mcp_servers]` header;
- `[[mcp_servers.wenyuan-memory]]` arrays (not emitted, but a hand-repaired install is indistinguishable).

Upstream decides ownership **from the parsed document**, not by header text (`H:1317-1329`, `:1343-1383`) and validates the result parses before writing (`H:1418-1424`). **Fix location:** `toml-table.ts:46-67` (+ a final `smol-toml`-equivalent parse guard in `registerTomlTable`, `:108-129`).

### T3 — P2 — TOML edits drop the file's trailing newline convention and strip all trailing whitespace

`toml-table.ts:102` `stripped.replace(/\s*$/, "")` removes every trailing blank line (user's deliberate grouping at EOF), and `:127`/`:151` re-add exactly one. Cosmetic, but it makes the "byte for byte otherwise" claim at `:19-22` false.

### R1 — P1 — `registerMcpServer`/`removeMcpServer` default `executable` to the host id

`src/mcp/register.ts:111-115,133-137`: `executable: input.executable ?? input.hostId`. No host adapter passes `executable` (`claude-code.ts:61-66`), so `spawn("claude-code", …)` always ENOENTs and every install takes the (fake-successful) fallback. One line in `mechanisms-types.ts:38-51` should carry the CLI name alongside `mechanism`.

### R2 — P2 — `inspectMcpServer` claims to let installs refuse a foreign entry before writing, but nobody calls it

`src/mcp/register.ts:67-101` (doc comment) vs. no caller in `src/` (grep: only `src/index.ts:113` re-exports it and `tests/unit/mcp-register.test.ts` uses it). The guard it describes is implemented independently inside `writeEntry` (`json-server-map.ts:99-108`), which is the one that actually runs; the two duplicate a policy that will drift.

### S1 — P1 — the skill directory is `wenyuan-memory`, the package name is `wenyuan-coding-agents`

`src/hosts/skill-locations.ts:23` `SKILL_DIRECTORY_NAME = OWNERSHIP_MARKER` → `…/skills/wenyuan-memory/`, while `package.json` name is `wenyuan-coding-agents` and upstream keeps the two distinct on purpose (`skill-dirs.ts`, `H:318`: `hindsight-coding-agent`). Within wenyuan this is *consistent* (directory ↔ `SKILL.md` front-matter name, `skill/SKILL.md:2`), so it is not a break for Anthropic-style skill loading; the cost is that `wenyuan-memory` now names three different things (the skill, the MCP server entry, the runtime directory, `src/runtime/layout.ts:17`). Uninstall/refresh (`skill-refresh.ts:30`) share the constant, so no divergence bug — P2 unless the reviewer values name separation. Flagged because upstream's skill-removal comment (`H:469-472`) shows why a *shared* name across hosts is dangerous: `~/.agents/skills` is shared by codex and (upstream) dsh, and `removeInstalledSkill` (`src/hosts/skills.ts:95-105`) would delete the other host's copy — wenyuan avoids this only because it declares `~/.dsh/skills` (§2.4); if dsh is corrected to `~/.agents/skills`, uninstalling either host will delete the other's skill.

### SEL1 — P2 — `select.ts` accepts `all` mixed with explicit names and silently drops the names

`src/hosts/select.ts:43-54`: `if (names.includes(ALL_HOSTS_KEYWORD))` returns the detected set and **ignores** the other arguments (`install all codex` on a machine without codex wires everything detected and reports success). Upstream `run()` treats `all` the same way (`H:2376` onward) but documents "an explicit target — `all`, or one or more harness names" (`README:58-59`); wenyuan's usage string (`README.md:25-26`) says `install all` *or* names. Reject the mix.

### LT1 — P2 — duplicate constants and a dead export

`STOP_DEADLINE_MARGIN_SECONDS` is declared twice (`src/hosts/lifecycle.ts:46`, `src/runtime/deadline.ts:19`), and `registrationFor` is imported and re-exported by `src/runtime/hook-host.ts:1,39` while `hostLifecycle()[stage]` is used directly in `src/runtime/deadline.ts:31`. Two sources of the same contract is what upstream's single `HOOK_HARNESSES` was created to prevent (`hook-lifecycle.ts:1-5`).

---

## 4. Fix list

**P0**
1. `src/hosts/hook-shape.ts:29,73-80` — emit Claude/Codex/CodeBuddy's documented shape `{"<Event>":[{"hooks":[{"type":"command","command":"node \"<abs script>\"","timeout":<s>}]}]}`; keep the budget in seconds per host (`lifecycle.ts:42-43`) or declare the unit per host like `HookTimeoutSpec.timeoutUnit` (`hook-lifecycle.ts:60-67`).
2. `src/hosts/lifecycle.ts:28-32` — per-host hook script names (`claude-hook.js` → `codex-hook.js` …) so each host's stdin/stdout envelope is the one it expects; remove the `HOST_LIFECYCLES` fallback in `src/runtime/hook-host.ts:35`.
3. `src/hosts/codex.ts:30` + `README.md:37` — write hooks to `~/.codex/hooks.json` (`H:652`), not `~/.codex/settings.json`; add `[features] hooks = true` seeding (`H:667-677`) to `src/mcp/toml-table.ts:108-129`.
4. `src/hosts/hooks.ts:121-127` — honour `edit.conflict` and fail (or skip with a message) when a foreign non-list member owns the event; `src/write/jsonc-scan.ts:307-321` — append after the array's last element, never mid-array.
5. `src/mcp/ownership.ts:43-66` — ownership by packaged-server path shape (basename `mcp-server.mjs` under `wenyuan-memory/…`), not exact `serverPath` equality, so re-install/uninstall repair a moved runtime (`H/src/core/util.ts:78-89`).

**P1**
6. `src/write/backup.ts:29-39` / `src/hosts/hooks-remove.ts:45` — never create a backup on a removal path.
7. `src/hosts/hooks-remove.ts:69-88` — delete a host config only when this package created it; otherwise prune the container and keep the file.
8. `src/write/jsonc-edit.ts:211-249,293-311` — recompute element ranges after each splice; `src/write/jsonc-scan.ts:323-351` — do not orphan trailing comments on delete.
9. `src/mcp/json-server-map.ts:93-120` — refuse a `mcpServers` value of the wrong type and an unparseable file instead of silently replacing them.
10. `src/mcp/command-line.ts:33-50` — `--env` before the `--` terminator; `:90-99` — fallback returns `ok:false`; `src/mcp/register.ts:111-115,133-137` + `mechanisms-types.ts` — carry the host's CLI name (`claude`), not the host id.
11. `src/mcp/toml-table.ts:46-67` — strip from a parsed document (dotted keys, `[mcp_servers]` sub-tables, `[[…]]`), and validate the result parses before writing (`:108-129`).
12. `src/mcp/ownership.ts:51-55` — require `args[0]` to be the packaged server.
13. `src/hosts/dsh.ts:40-43,77` — resolve `DSH_HOME` (`H:1743-1745`); point the skill at `~/.agents/skills` (`H/src/core/skill-dirs.ts:29-30`) and make same-name removal safe for the shared root (or keep the host-specific dir and document why).

**P2**
14. `src/hosts/types.ts:19-32` + `codex.ts:31` — drop the `json` whole-document rewrite path (`hooks.ts:139-149`) until a host actually needs it; when it is needed, copy upstream's `writeJsonc` per-key approach (`H:194-209`).
15. `src/mcp/toml-table.ts:102,127,151` — preserve trailing newlines/blank lines; make the strip drop the separator blank line it added.
16. `src/hosts/skill-locations.ts:23` + `src/hosts/skill-tools.ts:9` — decide the skill-directory name (package vs. ownership marker) once and document it; `src/hosts/select.ts:43-54` — reject `all` combined with explicit names.
17. `src/mcp/ownership.ts:75-85` — correct manual line (`claude mcp add --scope user <name> --env K=V -- node <path>`); `src/mcp/register.ts:67-101` — either call `inspectMcpServer` from the adapters or delete it; `src/hosts/lifecycle.ts:46` vs `src/runtime/deadline.ts:19` — single constant.

---

## Summary

- **Host inventory:** upstream wires 22 harnesses (23 names with the `agy` alias); wenyuan wires 4 and is missing 18, tabulated above with path, event names, shape, timeout+unit, MCP mechanism and skill directory.
- **Codex:** upstream writes `~/.codex/hooks.json` + `[features]`/`[mcp_servers.hindsight]` in `~/.codex/config.toml` (`installer.ts:648-710`, asserted at `installer.test.ts:1303-1313`). wenyuan's `~/.codex/settings.json` (`codex.ts:30`, `README.md:37`) is wrong: Codex does not read that file, so `install codex` leaves Codex unwired while reporting success.
- **14 distinct defects** across the scoped modules: **5 P0** (shape/command, per-host entry points, Codex path+flag, JSONC merge corruption, ownership-after-move), **9 P1**, **5 P2**.
- The unifying root cause behind most P0/P1: wenyuan generalises where upstream specialises per host (`hook-lifecycle.ts` vs. wenyuan's one `matchers` encoder and one `stripOurTable`), so each host's real format is approximated by a guess. Upstream's single-source `HOOK_HARNESSES` table is the model to copy.