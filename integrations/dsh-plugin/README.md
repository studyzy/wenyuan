# @studyzy/dsh-wenyuan-memory

WenYuan agent memory for the [DeepSeek Harness](https://github.com/deepseek-ai/deepseek-harness),
as a Cordis plugin bundle: one npm package that the profile composes, with its
own configuration card on the WebUI **Plugins** page.

This replaces the Claude-Code-style `SessionStart` / `UserPromptSubmit` / `Stop`
command hooks the installer's `dsh` host used to write. DSH is plugins all the
way down, so the memory wiring belongs on the harness's own extension points,
in-process — no subprocess per prompt, no hook bridge, no external hook config
file to keep in step.

| Half | Outlet | Role |
| --- | --- | --- |
| Host | `.` | Attaches recalled memory on `agent/created` and `agent/pre-step`, writes the session back on `agent/turn-stopping`, registers the eight `wenyuan_*` memory tools. |
| Browser | `./client` | The configuration card: server URL, memory bank, API key, and the two behaviour switches. |

## What it does

- **Context in.** When an agent is created the bank's knowledge is attached as
  context, so the session starts knowing what this repository already settled.
- **A prompt-time recall.** The first prompt of a turn that carries new user
  text is recalled and attached as that step's context. Each turn recalls once,
  so a tool loop does not pay per step.
- **A write-back.** When a turn stops, everything not yet handed over is
  assembled into the reference's own transcript document and retained under one
  ref-id: user and assistant text, one action line per tool call, injected memory
  stripped. A later turn appends only what is new.
- **Eight memory tools**, registered globally.
  - Read the curated knowledge pages: `wenyuan_search_knowledge_pages`,
    `wenyuan_list_knowledge_pages`, `wenyuan_read_knowledge_page`.
  - Write: `wenyuan_capture_initiative` (a tracked initiative),
    `wenyuan_ingest_document` (durable notes, and the correction mechanism).
  - Answer a question across everything recorded: `wenyuan_reflect`.
  - Check the memory itself: `wenyuan_diagnose` (what the bank holds and why
    queued work failed) and `wenyuan_sync_status` (whether this session's
    writes have finished processing).

Everything is fail-open: an unreachable or slow service costs the session its
memory, never its run.

### The write-back format

The assembled document is the one the reference's Claude Code `Stop` hook
produces — the difference is only where the turns come from (the commits folded
in memory rather than a transcript file on disk):

1. a `REF-ID: conversation:<sessionId>` line;
2. then one JSON object per line, `{ role, content, timestamp }`, where `role`
   is `user`, `assistant`, or `action`;
3. an `action` turn is the tool name plus its first recognizable target
   (`file_path`, `path`, `command`, `pattern`, … capped at 100 characters), never
   the tool's output;
4. every block this plugin injected is removed first, so a recall attached to
   turn N never becomes part of what turn N "said". Without that the bank slowly
   accumulates its own output and every later recall matches it.

The retain uses `strategy: "conversation"`, `tags: ["source:chat", "harness:dsh"]`,
and `metadata: { source, session_id, ref_id, harness }`.

## Install

```sh
# as a profile bundle (the same way any dsh bundle is installed)
dsh plugin --profile web add file:/path/to/integrations/dsh-plugin
# or from git
dsh plugin --profile web add git+https://github.com/studyzy/wenyuan.git#path:integrations/dsh-plugin
```

The package declares `dsh.bundle` (`cordis.patch.yml`, which mounts the Host
entry) and `dsh.client` (`exports["./client"]`, the browser bundle), so the
profile discovers both halves from one install. The Host entry id, and therefore
the settings namespace, is `dsh-wenyuan-memory`.

## Configure

The card appears on the WebUI **Plugins** page twice — as the plugin's own card
and, because the bundle is also a profile dependency, on the installed package's
page. Both surfaces edit the same form.

| Setting | Default | Meaning |
| --- | --- | --- |
| Server URL | `http://127.0.0.1:18888` | WenYuan base URL, without the `/v1` prefix |
| Memory bank | `default` | the bank this session reads and writes |
| API key | — | written through the credentials domain as `WENYUAN_API_KEY`; never read back into the form |
| Attach memory to prompts | on | the `agent/created` / `agent/pre-step` recall |
| Write sessions back | on | the `agent/turn-stop` retain |

Fields reach the page only because the Host schema marks them `volatile()`. The
API key does not ride the settings section at all: the card writes it through
the credentials domain, which answers "configured" or not and never returns the
value. A deployment that exports `WENYUAN_API_TOKEN` behind the service keeps
working without anyone touching the card — that reference is the fallback.

## Development

```sh
pnpm install
pnpm typecheck
pnpm test          # unit lane, offline
pnpm build         # tsc declarations + tsdown: lib/index.js, lib/client.js
pnpm verify:client # execute lib/client.js through a stand-in module loader
pnpm check         # all four
```

`pnpm test` needs no harness and no memory service: the HTTP client is exercised
against an injected `fetch`, the card against a stub scope, and the plugin
against a stand-in Cordis context. `pnpm verify:client` is the gate that catches
the failures the browser would otherwise surface first: a bundle that requires a
module the shell does not seed, or a configuration card registered under an `id`
instead of the package-name `key` (which registers without error and then never
renders).

`node_modules` may need `NODE_ENV=development` to install the dev lane, since
this machine's environment sets `NODE_ENV=production` globally.

## Layout

```
src/index.ts            the Hook/plugin entry: lifecycle listeners, the knowledge tools, settings resolution
src/client.ts           the WenYuan HTTP client (fetch only, fail-open)
src/transcript.ts       transcript turns and the reference's session document
src/transcript-projection.ts  the host-only fold that the write-back reads
src/context-blocks.ts   the injected-block tags and the strip
src/settings.ts         field names, defaults, and the bank-scoped route
src/source.ts           this plugin's MessageSourceMap entry
src/browser/            the configuration card: controller, component, locales, slot declarations
scripts/verify-client-bundle.mjs  the browser-kernel contract check
```

## Known limits

- The `wenyuan_*` tools are the tool surface; the server's fuller MCP roster is
  not mirrored here (the installer's MCP registration still covers a deployment
  that wants it).
- The write-back cursor is per process: after a harness restart, the next stop
  writes the session's folded turns again rather than appending from a persisted
  position.
- A subagent session is skipped on both seams: it shares its parent's turn, so
  injecting there would fight the parent's recall and write the same transcript
  twice.