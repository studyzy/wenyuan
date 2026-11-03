import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import {
  detectLegacyClaudePlugin,
  legacyClaudePluginWarning,
  readLegacyEndpoint,
} from "../../../src/core/legacy";
import { OWNERSHIP_MARKER } from "../../../src/write/marker";

const homes: string[] = [];

function homeWithStored(config: unknown): string {
  const home = mkdtempSync(join(tmpdir(), "wy-legacy-"));
  homes.push(home);
  if (config !== undefined) {
    mkdirSync(join(home, ".wenyuan"), { recursive: true });
    writeFileSync(join(home, ".wenyuan", "config.json"), JSON.stringify(config));
  }
  return home;
}

afterEach(() => {
  while (homes.length) rmSync(homes.pop()!, { recursive: true, force: true });
});

describe("readLegacyEndpoint", () => {
  it("is undefined when the pre-port CLI was never configured", () => {
    expect(readLegacyEndpoint(homeWithStored(undefined))).toBeUndefined();
  });

  it("carries a self-hosted server and its token", () => {
    const e = readLegacyEndpoint(
      homeWithStored({ serverMode: "self-hosted", apiUrl: "http://box:18888", apiToken: "t" })
    );
    expect(e?.serverMode).toBe("self-hosted");
    expect(e?.apiUrl).toBe("http://box:18888");
    expect(e?.apiToken).toBe("t");
  });

  it("honours the stored server mode", () => {
    const e = readLegacyEndpoint(
      homeWithStored({ serverMode: "hosted", apiUrl: "https://wenyuan.example/api" })
    );
    expect(e?.serverMode).toBe("hosted");
    expect(e?.apiUrl).toBe("https://wenyuan.example/api");
  });

  // The pre-port flow treated an absent URL as "use the local service on apiPort", so a config
  // that never sets one describes a local deployment rather than an unconfigured user.
  it("reads an absent URL as local mode", () => {
    const e = readLegacyEndpoint(homeWithStored({ serverMode: "local", bankId: "repo" }));
    expect(e?.serverMode).toBe("local");
    expect(e?.apiUrl).toBeUndefined();
  });

  it("carries a non-default local port, since in local mode the port is the endpoint", () => {
    expect(readLegacyEndpoint(homeWithStored({ serverMode: "local", apiPort: 19443 }))?.apiPort).toBe(
      19443
    );
    expect(
      readLegacyEndpoint(homeWithStored({ serverMode: "local", apiPort: 18888 }))?.apiPort
    ).toBeUndefined();
  });

  // A config we cannot parse is not a decision we can honour — fall through to the normal flow
  // rather than guessing an endpoint.
  it("ignores an unparseable config", () => {
    const home = mkdtempSync(join(tmpdir(), "wy-legacy-"));
    homes.push(home);
    mkdirSync(join(home, ".wenyuan"), { recursive: true });
    writeFileSync(join(home, ".wenyuan", "config.json"), "{not json");
    expect(readLegacyEndpoint(home)).toBeUndefined();
  });

  // Connection settings are deliberately the only thing carried over.
  it("carries no behavioural settings", () => {
    const e = readLegacyEndpoint(
      homeWithStored({
        serverMode: "self-hosted",
        apiUrl: "http://box:18888",
        bankId: "some-bank",
        logLevel: "debug",
      })
    );
    expect(Object.keys(e ?? {}).sort()).toEqual(["apiUrl", "harness", "serverMode", "source"]);
  });

  it("resolves any host id through the shared stored file", () => {
    const home = homeWithStored({ serverMode: "self-hosted", apiUrl: "http://cx:18888" });
    const e = readLegacyEndpoint(home, ["codex"]);
    expect(e?.harness).toBe("codex");
    expect(e?.apiUrl).toBe("http://cx:18888");
  });

  it("falls back to any known host when the preferred one has no entry", () => {
    const home = homeWithStored({ serverMode: "self-hosted", apiUrl: "http://cx:18888" });
    expect(readLegacyEndpoint(home, ["cursor-cli"])?.apiUrl).toBe("http://cx:18888");
  });
});

describe("detectLegacyClaudePlugin", () => {
  /** A fake ~/.claude whose settings.json carries the given hooks block. */
  function claudeDir(settings: unknown): string {
    const dir = mkdtempSync(join(tmpdir(), "wy-claude-"));
    homes.push(dir);
    if (settings !== undefined) writeFileSync(join(dir, "settings.json"), JSON.stringify(settings));
    return dir;
  }
  const runtimeDir = join(tmpdir(), "wy-claude-home", ".wenyuan", OWNERSHIP_MARKER);
  const hookCommand = (script: string) => `node "${join(runtimeDir, "hooks", script)}"`;
  const matcherGroup = (script: string) => ({
    hooks: {
      SessionStart: [{ hooks: [{ type: "command", command: hookCommand(script), timeout: 10 }] }],
    },
  });

  it("finds a registration naming a staged pre-port hook script", () => {
    const dir = claudeDir(matcherGroup("session-start.mjs"));
    expect(detectLegacyClaudePlugin("/any/repo", dir)).toBe(runtimeDir);
  });

  it("finds one inside a plain command entry (flat host shapes share the detection)", () => {
    const dir = claudeDir({ hooks: { sessionStart: { command: hookCommand("pre-prompt.mjs") } } });
    expect(detectLegacyClaudePlugin("/any/repo", dir)).toBe(runtimeDir);
  });

  it("ignores foreign hook commands, including ones naming a similar directory", () => {
    const dir = claudeDir({
      hooks: {
        SessionStart: [
          {
            hooks: [
              {
                type: "command",
                command: `node "${join(tmpdir(), "other-wenyuan-memory-x", "hooks", "session-start.mjs")}"`,
                timeout: 10,
              },
            ],
          },
        ],
      },
    });
    expect(detectLegacyClaudePlugin("/any/repo", dir)).toBeUndefined();
  });

  it("ignores our marker outside a hook script path (an MCP entry is not a hook install)", () => {
    const dir = claudeDir({
      mcpServers: { wenyuan: { command: "node", args: [join(runtimeDir, "mcp-server.mjs")] } },
    });
    expect(detectLegacyClaudePlugin("/any/repo", dir)).toBeUndefined();
  });

  it("is undefined when Claude has no settings file or it is unreadable", () => {
    const dir = mkdtempSync(join(tmpdir(), "wy-claude-"));
    homes.push(dir);
    expect(detectLegacyClaudePlugin("/any/repo", dir)).toBeUndefined();
    writeFileSync(join(dir, "settings.json"), "{not json");
    expect(detectLegacyClaudePlugin("/any/repo", dir)).toBeUndefined();
  });

  it("the warning names the runtime and both ways out", () => {
    const warning = legacyClaudePluginWarning(runtimeDir);
    expect(warning).toContain(runtimeDir);
    expect(warning).toContain("wenyuan-agents install");
    expect(warning).toContain("wenyuan-agents uninstall");
  });
});
