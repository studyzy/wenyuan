# wenyuan-coding-agents

Installs the WenYuan memory service into coding-agent hosts.

The service already serves MCP; this package is what wires it into a host so a
session can use it. Per selected host, `install` does three things:

1. merges three lifecycle hook registrations into the host's own config file —
   session start, before a user prompt is submitted, and stop;
2. writes the MCP server entry into the host's MCP config location;
3. copies the packaged companion skill into the host's skills directory.

Every write into a host-owned file is non-destructive: the existing file is
read and merged rather than replaced, a backup copy is taken the first time a
pre-existing file is touched, and running the same command again leaves the
file as the first run left it. `uninstall` removes exactly what was written and
leaves foreign entries alone. `update` refreshes the packaged runtime and
writes no host config at all. `stats` is read-only.

## Usage

```sh
wenyuan-agents install all                  # every detected host
wenyuan-agents install claude-code codex    # exactly these hosts
wenyuan-agents uninstall codebuddy
wenyuan-agents update                       # re-stage the runtime only
wenyuan-agents stats
```

Flags:

| Flag | Meaning |
| --- | --- |
| `--server <mode>` | `hosted`, `self-hosted` or `local` |
| `--api-url <url>` | service address, required by the self-hosted mode |
| `--api-token <token>` | bearer token, required by the hosted mode |
| `--import-conversations` | additionally attempt the past-session backfill |
| `--enable-workspace-mcp` | authorize flipping a host's global workspace-MCP setting |

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
src/hosts/     the registry, host selection, one module per host, shared hook and skill behavior
src/commands/  install, uninstall, update and stats
src/cli/       argument parsing, usage text and the dispatcher
hooks/         one packaged lifecycle-hook entry point per event
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
```

The unit suite runs offline against a temporary fake home; the end-to-end suite
drives the built entry point in-process against the same fixture.

## Known gaps

- The packaged lifecycle hooks exist so a host has something to launch by
  absolute path; their runtime behavior is a later slice.
- The skill's substance, structure and front matter are placeholders.
- The per-host field spellings and budget units for the lifecycle
  registrations, and the per-host shape of the MCP entry beyond the
  caller-identifying variable, are deferred to later slices.
- Nothing in this slice writes the usage log `stats` reports on.
