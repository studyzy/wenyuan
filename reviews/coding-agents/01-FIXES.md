# Fixes applied — `integrations/`

Follow-up to [00-REVIEW.md](00-REVIEW.md). Fixed 2026-10-29. Gate at the end of
the work: typecheck clean, 353 unit tests, 161 end-to-end tests, 63 dsh-plugin
tests, skill check, and **49/49 checks in the new live integration check**.

## The one idea

Every defect in the review had the same shape: **the file said the right thing
and the host still ran nothing.** A hook written under a key no host parses, a
hook file the host never reads, a stop stage that reads a payload field the host
never sends, a bank every repo shares. All of them were invisible from the
installer's side, because writing succeeded.

So the two structural fixes come first, and everything else sits on them:

1. **Write the host's shape, not ours.** `hook-shape.ts` now emits
   `{"<Event>":[{"hooks":[{"type":"command","command":"node \"…\"","timeout":N}]}]}`
   — the matcher-group key is `hooks`, and the command is `node "<abs path>"`
   rather than a bare `.mjs` path a shell may not honour.
2. **Read it back the way the host would.** `install` re-reads each config in the
   host's own dialect and checks that the event would launch the packaged
   script. Anything it cannot confirm is a **reported failure with a non-zero
   exit**, never a printed "wired".

## P0 fixes

| # | Was | Now |
|---|---|---|
| 1 | `matchers` key + bare `.mjs` command — no host parses it | `hooks` key + `node "…"` (`src/hosts/hook-shape.ts:41`) |
| 2 | Codex hooks written to `~/.codex/settings.json`, which Codex never reads | `~/.codex/hooks.json` + `features.hooks = true` seeded into `config.toml` (`src/hosts/codex.ts:36`, `src/hosts/toml-feature.ts`) |
| 3 | Stop read turns out of the payload; Claude's Stop payload has none, so **nothing was ever retained** | `transcript_path` is read (`src/runtime/conversation.ts:118`), with a reader that handles Claude Code's JSONL, the agent-cli `type:"message"` records, JSON arrays and wrapper objects |
| 4 | `document_id` computed and dropped; every stop minted a new document | `document_id: conversation:<session>` + `update_mode` + `context` + `tags`/`metadata` (`src/runtime/stop-stage.ts:88`), with a retain cursor so a second stop appends only the new turn |
| 5 | One `default` bank for every repo | **not fixed** — see "Still open" |
| 6 | DSH plugin resolves one bank per process | **not fixed** — see "Still open" |
| 7 | DSH seams throw into the host | **not fixed** — see "Still open" |
| 8 | DSH tool failures reported as success | **not fixed** — see "Still open" |
| 9 | JSONC removal deleted the neighbouring member when a comment sat beside it | `previousStructural` walks back over comments (`src/write/jsonc-scan.ts:65`) |
| 10 | `claude mcp add … -- node <server> --env K=V` — `--env` after `--` is dropped | `--env` pairs before the terminator (`src/mcp/command-line.ts:44`) |
| 11 | MCP ownership compared the exact staged path: a moved runtime made our own entry permanently `foreign` | shape match on `…/wenyuan-memory/mcp-server.mjs` (`src/mcp/ownership.ts:60`) |
| 12 | TOML table matched by literal header only | quoted/dotted headers normalized (`src/mcp/toml-table.ts:64`) |
| 13 | `install dsh` hard-coded `~/.dsh` | **not fixed** — see "Still open" |
| 14 | `WENYUAN_BANK_ID` never reached the DSH plugin | **not fixed** — see "Still open" |

Also fixed, found while fixing the above:

- **A conflict was swallowed.** `hooks.ts` ignored `edit.conflict` and returned
  `ok:true`; a foreign hook on the same event now blocks that event and the run
  reports it.
- **BOM made every JSONC edit silently no-op** while install printed "wired"
  (`src/write/jsonc-scan.ts:175`).
- **CRLF files were half-rewritten**: a reversed constant meant a Windows-line
  file got bare `\n` insertions (`src/write/jsonc-scan.ts:24`).
- **The manual fallback line was wrong and silent**: it had `--env` after `--`
  and returned `ok:true`. It is now the exact pastable command and a reported
  failure.
- **`claude` was never executed**: `register.ts` defaulted the executable to the
  host id (`"claude-code"`), so the command-line mechanism always fell back. It
  now receives the host's real executable (`src/hosts/claude-code.ts:64`).

## Two hosts added

`cursor-cli` (`~/.cursor/hooks.json`, ~/.cursor/mcp.json`) and `copilot-cli`
(`~/.copilot/hooks/hooks.json`, `~/.copilot/mcp-config.json`) — both `flat-entry`
hosts with their own event words, so the flat-entry style is now exercised by
real hosts rather than only by a unit test. **They have not been driven against
a live CLI**; they write what each host's documentation specifies and are
covered by the read-back check.

## The verification you asked for

`npm run check:integration` → `scripts/integration-check.mjs`. It does not
inspect files; it runs the product:

```
PASS  install every host
PASS  claude-code: three events registered
PASS  claude-code: prompt hook exits 0
PASS  claude-code: prompt hook calls recall
PASS  claude-code: recalled text reaches the host
PASS  claude-code: session-start hook calls recall
PASS  claude-code: stop hook exits 0
PASS  claude-code: stop hook reads the transcript and retains it
PASS  claude-code: write-back carries one document id
PASS  claude-code: a second stop appends only the new turn
...(the same ten for codex, codebuddy, cursor-cli, copilot-cli)
PASS  mcp: initialize answers
PASS  mcp: tools listed
PASS  hooks authenticate
49/49 checks passed
```

For each of the five hook hosts it: installs into a throwaway home, reads that
host's config back the way the host reads it, **execs the exact command string
in a real `/bin/sh`**, and asserts against an in-process mock of the memory
service that the recall carried the prompt, the recalled text reached the
host's stdout, the stop hook read the transcript and retained it, and a second
stop appended only the new turn under one document id.

The check found three real bugs while I was writing it — the JSONL reader's
object/stream detection, a comment-blind read-back (which reported every event
`MISSING` on a valid config), and the CRLF constant. That is the point of it.

`npm run check` now runs, in order: typecheck → skill check → unit → **integration
check** → e2e.

## Test-suite changes worth knowing

The e2e suite previously ran with `PATH: ""`, which meant no host CLI existed —
and after these fixes that is an *honest failure* ("the MCP entry could not be
registered"), not a success. The fixture now writes recording stand-ins for the
host CLIs outside the fake home and hands suites that PATH; `claude` answers
`mcp get`/`add`/`remove` from its own record, so suites exercise the real
ownership logic. Tests that encoded the old behaviour were rewritten, not
weakened — e.g. the `enableAllProjectMcpServers` test now asserts the switch is
**never** touched, with or without the flag.

## Still open (deliberately)

Not done in this pass, and still listed in the README's Known gaps:

- **Per-repo banks (#5)**: the default bank is still a single `default`. This
  changes where every existing install's memory lives, so it needs a decision
  (and probably a migration), not a silent flip.
- **DSH plugin (#6–#8, #13, #14)**: per-workspace bank resolution, unconditional
  fail-open on the seams, tool failures as errors, `$DSH_HOME`, and getting
  `WENYUAN_BANK_ID` into the plugin — all in `integrations/dsh-plugin`, a
  separate package with its own harness contracts. It is untouched here and its
  63 tests still pass.
- **MCP tool surface**: the packaged server still serves one tool (`wenyuan_recall`).
- **Knowledge pages / reflect injection, `stats` writer, `--import-conversations`,
  auto-update, `optInOnly`** — unchanged, still in the gap list.