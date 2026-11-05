# Full port of the coding-agents integration module

Date: 2026-11-03
Status: approved for implementation (user decision 2026-11-03)
Initiative page: kp-3214e80064cb44dfbfcc246005665d29

## Goal

Bring integrations/coding-agents to full functional parity with the reference
integration module it replaces: one npm package (@studyzy/wenyuan-coding-agents,
CLI wenyuan-agents) that wires 22 coding-agent hosts to a wenyuan server — 16
hook hosts through prebuilt per-host hook binaries over a shared core runtime,
and 7 persistent-plugin hosts through in-process adapters — plus the full
background-intelligence layer. integrations/dsh-plugin is absorbed; the
separate package disappears.

## Scope decisions (2026-11-03)

1. All 22 hosts. Hook hosts: claude-code, codex, codebuddy, cursor-cli,
   copilot-cli, workbuddy, dcode, antigravity-cli, devin-cli, grok-build,
   qwen-code, zcode, traecode, factory-droid, kimi, droid. Persistent-plugin
   hosts: opencode, opencode2, kilo, cline-cli, dsh, pi, prime-agent.
2. Background intelligence fully included: git-history backfill, cold-repo
   survey (detached headless agent), missions (retain strategies + knowledge-page
   taxonomy), deepen, chat write-back schema, staged-runtime auto-update.
3. Hook delivery switches to prebuilt bins + shared core. The current
   install-time generated hook scripts (~/.wenyuan/wenyuan-memory/hooks/*.mjs)
   are replaced: core logic ships in the package dist/, install stages it under
   ~/.wenyuan/wenyuan-memory/ and every host registration points at the staged
   copy; auto-update keeps the staged copy current.
4. No browser settings card. The dsh-plugin browser half is dropped;
   configuration comes from env/config files only. The dsh adapter uses
   structural typing with zero @deepseek-ai/* runtime dependencies — any dsh
   whose event names still match can load it, with no version to keep in step.

## Deliberate deviations from the reference

- Local daemon mode is cut. The reference ships a daemon that runs its own
  embedded memory server on this machine via a separate all-in-one package.
  wenyuan IS the server, so serverMode "daemon" has no counterpart; the client
  only ever points at a wenyuan URL (self-hosted or remote).
- Naming follows the clean-room naming table below; nothing in code or docs may
  carry the reference's name (the leakage scanner enforces the token).
- Bank naming stays <tenant>::<project> per the multi-tenant design; the
  per-workspace runtime cache maps one runtime instance per repo root, bank
  resolved per session workspace (one host process can serve many workspaces).

## Naming (clean-room)

All owned naming is wenyuan-side; the reference's corresponding spellings are
not written out here (nothing in code or docs may carry the reference's name).

| Item | wenyuan naming |
|---|---|
| knowledge tool names (8) | wenyuan_* (search/list/read knowledge pages, capture initiative, ingest document, reflect, diagnose, sync status) |
| env keys | WENYUAN_* |
| npm package | @studyzy/wenyuan-coding-agents |
| CLI bin | wenyuan-agents |
| per-host hook bins | wenyuan-<host>-hook |
| staged runtime directory | ~/.wenyuan/wenyuan-memory (existing layout) |
| REF-ID tracer format | unchanged (server-side contract) |

## Target architecture

    integrations/coding-agents/
      src/
        core/                shared runtime, every host adapter imports here
          client layer:      api client, host-client (resolve server), bank
                             naming, config, paths, opt-in, legacy
          lifecycle layer:   hook runtime (one path: auto-inject once per
                             session, recall, retain), plugin runtime (same core
                             lifecycle, host-specific injection/toast/transcript),
                             session-start, session-cache, retain-hook/cursor/
                             stamp, inject, knowledge-injection, knowledge-tools
                             (8 tools), turn-journal, history
          transcript layer:  generic reader + 17 per-host readers (dsh via
                             zstd-framed JSONL)
          background layer:  git backfill + layout, seed, survey (detached
                             headless agent), missions, chat schema, deepen,
                             sync, auto-update
          misc:              attribution, brand, diag, log, status, usage,
                             skill-dirs, skill-sync, uuid, util, template, types
        <host>-hook.ts       16 hook hosts: thin bins delegating to the core
                             hook runtime (1-3 bins per host per its event set)
        <plugin>.ts          7 plugin adapters sharing harness/
        harness/             persistent-plugin framework: plugin-entry,
                             registry, hook-lifecycle
        installer.ts         install/uninstall/update/stats; stages runtime,
                             wires hooks/MCP/skills per host native mechanism
        install-ui.ts        interactive host selection
        mcp-server.ts        stdio MCP bin (hosts that want MCP instead of hooks)
        e2e/                 stub-model + per-host harness runner
      skill/                 generated companion skill (scripts/build-skill.mjs)

## Waves (clean-room batches, each with real verification)

| # | Wave | Contents | Gate |
|---|---|---|---|
| 1 | Core foundations | package skeleton (naming table applied), api client, host-client, bank, config/paths, log/diag, util/uuid/jsonl, opt-in/legacy | typecheck + unit; client contract test against served handler |
| 2 | Lifecycle runtime | hook runtime, plugin runtime, session-start/cache, retain-hook/cursor/stamp, inject, knowledge-injection, knowledge-tools, turn-journal, history | unit + one host (claude-code) wired end-to-end in one-shot HOME |
| 3 | Transcript readers | generic + 17 per-host readers (zstd frames for dsh) | per-reader unit tests on recorded shapes |
| 4 | Installer + hook hosts | installer rewrite (stage + prebuilt bins), install-ui, 16 hook hosts with their hook bins, MCP mechanisms kept, skill sync, auto-update | check:integration extended to every hook host in one-shot HOMEs |
| 5 | Harness + plugin hosts | harness/ (plugin-entry, registry, hook-lifecycle), 7 plugin adapters; delete integrations/dsh-plugin after the dsh adapter lands | per-plugin unit + dsh Cordis bundle check |
| 6 | Background intelligence | git backfill, seed, missions, deepen, chat, sync, survey | unit + live-server integration (PG); survey behind an explicit opt-in flag |
| 7 | Stub-model e2e | stub-model server, per-host harness runner (real CLIs, one-shot HOME/containers) | test:e2e green on hosts available in CI; others documented |

Dependency order is strict: wave N imports waves 1..N-1. Waves 5 and 6 can
proceed in parallel after wave 4.

## Migration notes

- install must remove the previously generated hook-script registrations it
  wrote under the old design (the ownership marker identifies them) before
  writing the new bin registrations — an upgraded machine must not recall twice.
- The dsh host in the installer stops calling the plugin forwarder for the
  standalone package and instead writes the profile patch row pointing at this
  package's staged dsh export; the old package name is removed from any profile
  that has it (idempotent swap).
- CI gains no new jobs; the existing coding-agents job keeps gating
  typecheck/build/unit, plus the e2e job behind the env guard.
- Skill tool identifiers in the generated skill stay wenyuan_*; the six tool
  ids named in the current skill are reconciled with the eight-tool suite.

## Risks

- Survey spawns real agent processes on cold repos; gate behind opt-in and a
  lease (the reference does both) so CI and tests never trigger it.
- Host CLI drift: transcript shapes change between host versions; every reader
  test pins a recorded shape, and the e2e runner is the drift alarm.
- check:integration is the safety net: it found three real bugs in the current
  design; every new host must be added to it in the same wave that adds the host.
- zstd for dsh transcripts needs Node 22.15+; declare the engines floor and
  degrade with a clear message, not a crash.
