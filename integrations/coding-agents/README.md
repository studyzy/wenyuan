# wenyuan-coding-agents

Installs the WenYuan memory service into coding-agent hosts.

The service already serves MCP; this package is what wires it into a host so a
session can use it. Per selected host, `install` does three things:

1. merges three lifecycle hook registrations into the host's own config file —
   session start, before a user prompt is submitted, and stop (a host whose
   wiring is a plugin of its own — DSH — gets the plugin installed instead);
2. writes the MCP server entry into the host's MCP config location, naming the
   packaged server that staging placed beside the hooks;
3. copies the packaged companion skill into the host's skills directory.

Every write into a host-owned file is non-destructive: the existing file is
read and merged rather than replaced, a backup copy is taken the first time a
pre-existing file is touched, and running the same command again leaves the
file as the first run left it. `uninstall` removes exactly what was written and
leaves foreign entries alone. `update` refreshes the packaged runtime and
writes no host config at all. `stats` is read-only.

**`install` verifies what it wrote.** After writing a host's hooks it re-reads
that file the way the host reads it and checks that each event would launch the
packaged script. An event it cannot confirm — a foreign entry occupying the
key, an entry in a shape the host does not parse, a file the host does not
read — is reported as a failure and `install` exits non-zero, because a host
that runs no hook has no memory regardless of what was written. The same rule
applies to the MCP entry: a registration that could not be made is reported
instead of assumed.

## Usage

```sh
wenyuan-agents install all                  # every detected host
wenyuan-agents install claude-code codex    # exactly these hosts
wenyuan-agents uninstall codebuddy
wenyuan-agents update                       # re-stage the runtime only
wenyuan-agents stats
```

Hosts: `claude-code`, `codex`, `codebuddy`, `cursor-cli`, `copilot-cli`, `dsh`.

| Host | Lifecycle hooks | MCP registration | Skill |
| --- | --- | --- | --- |
| `claude-code` | `~/.claude/settings.json` | `claude mcp add`, user scope | `~/.claude/skills` |
| `codex` | `~/.codex/hooks.json` + `features.hooks` in `config.toml` | `~/.codex/config.toml` table | `~/.agents/skills` |
| `codebuddy` | `~/.codebuddy/settings.json` | `~/.codebuddy/.mcp.json` (or `mcp.json`, `~/.codebuddy.json`) | `~/.codebuddy/skills` |
| `cursor-cli` | `~/.cursor/hooks.json` | `~/.cursor/mcp.json` | `~/.cursor/skills` |
| `copilot-cli` | `~/.copilot/hooks/hooks.json` | `~/.copilot/mcp-config.json` | `~/.copilot/skills` |
| `dsh` | none — a plugin owns these; see below | `~/.dsh/mcp.json` server map | `~/.dsh/skills` |

The matcher-group hosts (`claude-code`, `codex`, `codebuddy`) read the same
registration shape Claude Code does:

```json
{ "hooks": { "Stop": [ { "hooks": [ { "type": "command", "command": "node \"/…/stop.mjs\"", "timeout": 30 } ] } ] } }
```

`cursor-cli` and `copilot-cli` read one object per event instead
(`{"stop": {"command": "node \"…\"", "timeout": 30}}`) and spell the events
in their own lower-camel words.
Flags:

| Flag | Meaning |
| --- | --- |
| `--server <mode>` | `hosted`, `self-hosted` or `local` |
| `--api-url <url>` | service address, required by the self-hosted mode |
| `--api-token <token>` | bearer token, required by the hosted mode |
| `--import-conversations` | additionally attempt the past-session backfill |
| `--enable-workspace-mcp` | accepted for compatibility; the machine-wide project-MCP switch is never touched |

Connection parameters resolve in one fixed order: an explicit flag, then the
stored configuration file, then the deployment environment override, then the
built-in default. The stored configuration lives at `~/.wenyuan/config.json`
and is relocated by `WENYUAN_CONFIG`. Its keys are `serverMode`, `apiUrl`,
`apiToken`, `bankId` and `apiPort`; the environment overrides it honors are
`WENYUAN_API_URL`, `WENYUAN_API_TOKEN`, `WENYUAN_BANK_ID` and
`WENYUAN_API_PORT`. Nothing prompts without a terminal.

## Layout

```
src/write/     non-destructive write primitives: marker, merge, backup, comment-preserving edit
src/config/    per-user locations, the stored configuration, the precedence chain
src/runtime/   the stable per-user runtime directory and staging
src/mcp/       the MCP server registration and its ownership guard
src/hosts/     the registry, host selection, one module per host, shared hook and skill behavior,
               and the DSH plugin install
src/commands/  install, uninstall, update and stats
src/cli/       argument parsing, usage text and the dispatcher
hooks/         one packaged lifecycle-hook entry point per event, plus the MCP server
skill/         the packaged companion skill
```

Each host module declares three places — its config location, its MCP config
location and its skills directory — and nothing else in the host's state is
touched. Host config formats are not normalized: each host keeps its native
shape.

## Development

<!-- region:skill-usage -->
## Using the companion skill

Once installed, each host loads the packaged skill from its own skills root.
The skill instructs the agent to store deliberately, to retrieve search-first,
and to correct stale memory with a correction document — the same tool surface
the installed MCP server exposes.
<!-- /region -->

```sh
npm --prefix integrations/coding-agents install
npm --prefix integrations/coding-agents run typecheck
npm --prefix integrations/coding-agents run build
npm --prefix integrations/coding-agents test        # offline unit suite
npm --prefix integrations/coding-agents run test:e2e # builds, then drives the real installer
npm --prefix integrations/coding-agents run check:integration  # see below
```

The unit suite runs offline against a temporary fake home; the end-to-end suite
drives the built entry point in-process against the same fixture.

`check:integration` is the suite that answers "does a wired host actually reach
memory". It installs every host into a throwaway home, reads each host's config
back the way that host reads it, **execs the exact command string the host would
exec in a real shell**, and asserts against an in-process mock of the memory
service that the hook launched and made the right call: the recall carries the
prompt, the recalled text reaches the host's stdout, the stop hook reads the
host's transcript, and a second stop appends only the new turn under one
document id. It needs no server, no network and no credentials, and it is part
of `npm run check`.

A file that is written correctly is not the same thing as a host that runs it —
that distinction is what every defect this package has had in common, so the
check stops at the host's own execution boundary rather than at the file.

## Known gaps

- The packaged MCP server answers the protocol handshake and serves a single
  recall tool; the rest of the memory tool surface is a later slice.
- The stop stage reads the conversation out of the hook payload and does not
  yet read `transcript_path`, so on a host whose `Stop` payload carries no
  turns nothing is retained.
- The written document has no stable `document_id` yet, so a repeated write-back
  is not deduplicated.
- The skill's substance, structure and front matter are placeholders.
- Nothing in this slice writes the usage log `stats` reports on.
- Verified end to end here: Claude Code, Codex and DSH. The Cursor and Copilot
  adapters write the paths and shapes their own documentation gives and are
  covered by the read-back check, but have not been driven against a live CLI.
