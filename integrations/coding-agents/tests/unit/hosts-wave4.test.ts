import { describe, expect, it } from "vitest";
import { mkdtemp, readFile, writeFile, mkdir } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  PROCESS_COMMAND_TYPE,
  commandEntriesOf,
  encodeRegistration,
  entriesHostWouldRun,
  hostReadsOurEntry,
  registrationIsOwned,
} from "../../src/hosts/hook-shape";
import { registrationFor } from "../../src/hosts/lifecycle";
import {
  installKimiBlock,
  kimiBlockEvents,
  removeKimiBlock,
  renderKimiBlock,
} from "../../src/hosts/kimi-block";
import {
  prepareDcodeMarketplace,
  DCODE_FALLBACK_MARKETPLACE,
} from "../../src/hosts/dcode";
import {
  enableTraecodeWorkspaceMcp,
  removeTraecodeUserMcpEntry,
  traecodeUserDataDirectory,
} from "../../src/hosts/traecode";
import { installLifecycleHooks, hookPathFor } from "../../src/hosts/hooks";
import { OWNERSHIP_MARKER } from "../../src/write/marker";

/**
 * The wave-4 host dialects: the two new registration shapes, the marked TOML
 * block, the plugin marketplace preparation, and the per-host config seams the
 * shared writer applies. The shared machinery's own suites stay untouched;
 * everything here is what these hosts add.
 */

const STAGED = "/home/u/.wenyuan/wenyuan-memory";
const PATH = `${STAGED}/hooks/session-start.mjs`;

function registration(style: Parameters<typeof encodeRegistration>[0]["style"]): Parameters<typeof encodeRegistration>[0] {
  return { event: "SessionStart", scriptFile: "session-start.mjs", budget: 30_000, budgetUnit: "milliseconds", style };
}

describe("the process registration shape", () => {
  it("encodes the argv split a no-shell host spawns", () => {
    const value = encodeRegistration(registration("process-entry"), PATH, 30_000) as unknown as Record<string, unknown>;
    expect(value).toEqual({
      hooks: [
        {
          type: PROCESS_COMMAND_TYPE,
          command: "node",
          args: [PATH],
          timeoutMs: 30_000,
        },
      ],
    });
  });

  it("decodes a process entry to the script it runs", () => {
    const entry = { type: PROCESS_COMMAND_TYPE, command: "node", args: [PATH], timeoutMs: 30_000 };
    expect(commandEntriesOf(entry)).toEqual([{ type: PROCESS_COMMAND_TYPE, command: PATH, timeout: 30_000 }]);
    expect(registrationIsOwned(entry)).toBe(true);
    expect(registrationIsOwned({ type: PROCESS_COMMAND_TYPE, command: "node", args: ["/elsewhere/x.mjs"], timeoutMs: 1 })).toBe(false);
  });

  it("reads ZCode's written registration back the way ZCode runs it", () => {
    const written = {
      hooks: [{ type: PROCESS_COMMAND_TYPE, command: "node", args: [PATH], timeoutMs: 30_000 }],
    };
    expect(entriesHostWouldRun(written, "process-entry").map((entry) => entry.command)).toEqual([PATH]);
    expect(hostReadsOurEntry(written, PATH, "process-entry")).toBe(true);
    // A command-string entry on the same event is not a registration ZCode runs.
    expect(entriesHostWouldRun({ hooks: [{ type: "command", command: `node "${PATH}"`, timeout: 30 }] }, "process-entry")).toEqual([]);
  });

  it("encodes the plain entry with no execution type", () => {
    const value = encodeRegistration(registration("plain-entry"), PATH, 30) as unknown as Record<string, unknown>;
    expect(value).toEqual({ command: `node "${PATH}"`, timeout: 30 });
    expect(commandEntriesOf(value)).toHaveLength(1);
    expect(hostReadsOurEntry(value, PATH, "plain-entry")).toBe(true);
  });
});

describe("the Kimi TOML block", () => {
  it("renders one strict three-key table per declared event", () => {
    const block = renderKimiBlock("kimi", (scriptFile) => hookPathFor(scriptFile, STAGED));
    expect(block).toContain("# WENYUAN_CODING_AGENTS_KIMI_START");
    expect(block).toContain("[[hooks]]");
    expect(block).toContain('event = "SessionStart"');
    expect(block).toContain('event = "UserPromptSubmit"');
    expect(block).toContain('event = "Stop"');
    expect(block).toContain(`command = "node \\\"${STAGED}/hooks/session-start.mjs\\\""`);
    expect(block).toContain("timeout = 30");
    expect(block).toContain("timeout = 60");
    // The strict validator drops every hook on an unknown key.
    expect(block).not.toMatch(/^type = /m);
  });

  it("replaces a previous block on re-install and strips it on uninstall", async () => {
    const directory = await mkdtemp(join(tmpdir(), "kimi-block-"));
    const configPath = join(directory, "config.toml");
    await writeFile(configPath, "[mcp_servers.foreign]\ncommand = \"x\"\n");
    const first = renderKimiBlock("kimi", (scriptFile) => hookPathFor(scriptFile, STAGED));
    await installKimiBlock(configPath, first);

    // A re-install with a moved runtime replaces its own block, keeping foreign text.
    const moved = renderKimiBlock("kimi", (scriptFile) => hookPathFor(scriptFile, "/new/.wenyuan/wenyuan-memory"));
    await installKimiBlock(configPath, moved);
    const afterReinstall = await readFile(configPath, "utf8");
    expect(afterReinstall).toContain("[mcp_servers.foreign]");
    expect(afterReinstall).toContain("/new/.wenyuan/wenyuan-memory");
    expect(afterReinstall).not.toContain(STAGED);
    expect((afterReinstall.match(/# WENYUAN_CODING_AGENTS_KIMI_START/g) ?? []).length).toBe(1);

    expect(await removeKimiBlock(configPath)).toBe(true);
    const cleaned = await readFile(configPath, "utf8");
    expect(cleaned).toContain("[mcp_servers.foreign]");
    expect(cleaned).not.toContain("WENYUAN_CODING_AGENTS_KIMI");
  });

  it("parses the block back into the events it would run", () => {
    const block = renderKimiBlock("kimi", (scriptFile) => hookPathFor(scriptFile, STAGED));
    const events = kimiBlockEvents(block);
    expect(events.get("SessionStart")).toBe(`node "${STAGED}/hooks/session-start.mjs"`);
    expect(events.get("Stop")).toBe(`node "${STAGED}/hooks/stop.mjs"`);
    expect(events.size).toBe(3);
  });
});

describe("the Dcode marketplace preparation", () => {
  it("writes the conventional marketplace pointing at the staged runtime", async () => {
    const home = await mkdtemp(join(tmpdir(), "dcode-mkt-"));
    const source = await prepareDcodeMarketplace(home);
    expect(source).toBe(join(home, ".wenyuan"));
    const marketplace = JSON.parse(
      await readFile(join(home, ".wenyuan", ".agents/plugins/marketplace.json"), "utf8"),
    ) as { name: string; plugins: { name: string; source: { source: string; path: string } }[] };
    expect(marketplace.name).toBe("wenyuan-coding-agents");
    expect(marketplace.plugins).toHaveLength(1);
    expect(marketplace.plugins[0]?.source.path).toBe("./wenyuan-memory");
  });

  it("preserves a foreign marketplace and falls back to its own file", async () => {
    const home = await mkdtemp(join(tmpdir(), "dcode-mkt-"));
    const conventional = join(home, ".wenyuan", ".agents/plugins/marketplace.json");
    await mkdir(join(home, ".wenyuan", ".agents/plugins"), { recursive: true });
    await writeFile(
      conventional,
      JSON.stringify({ name: "someone-else", plugins: [{ name: "theirs" }] }),
    );
    const source = await prepareDcodeMarketplace(home);
    expect(source).toBe(join(home, ".wenyuan"));
    // The foreign manifest is untouched.
    const untouched = JSON.parse(await readFile(conventional, "utf8")) as { name: string };
    expect(untouched.name).toBe("someone-else");
    const fallback = JSON.parse(
      await readFile(join(home, ".wenyuan", DCODE_FALLBACK_MARKETPLACE), "utf8"),
    ) as { name: string };
    expect(fallback.name).toBe("wenyuan-coding-agents");
  });
});

describe("the TraeCode adapter helpers", () => {
  it("resolves the Electron user-data directory through the brand probe", () => {
    const home = "/home/u";
    const directory = traecodeUserDataDirectory(home);
    expect(directory.startsWith(home) || process.env.XDG_CONFIG_HOME !== undefined).toBe(true);
  });

  it("removes a stale user-level MCP entry of ours and nothing else", async () => {
    const home = await mkdtemp(join(tmpdir(), "trae-mcp-"));
    const mcpPath = join(traecodeUserDataDirectory(home), "User", "mcp.json");
    await mkdir(join(mcpPath, ".."), { recursive: true });
    await writeFile(
      mcpPath,
      JSON.stringify({
        mcpServers: {
          theirs: { command: "uvx", args: ["theirs"] },
          [OWNERSHIP_MARKER]: {
            command: "node",
            args: [`${STAGED}/mcp-server.mjs`],
            env: {},
          },
        },
      }),
    );
    expect(await removeTraecodeUserMcpEntry(home)).toBe(true);
    const after = JSON.parse(await readFile(mcpPath, "utf8")) as { mcpServers: Record<string, unknown> };
    expect(Object.keys(after.mcpServers)).toEqual(["theirs"]);
    expect(await removeTraecodeUserMcpEntry(home)).toBe(false);
  });

  it("flips the workspace gate in plain JSON and refuses a comment-bearing file", async () => {
    const home = await mkdtemp(join(tmpdir(), "trae-gate-"));
    expect(await enableTraecodeWorkspaceMcp(home)).toBe(true);
    const settings = JSON.parse(
      await readFile(join(traecodeUserDataDirectory(home), "User", "settings.json"), "utf8"),
    ) as unknown as Record<string, unknown>;
    expect(settings["trae.mcp.enableWorkspaceMcp"]).toBe(true);

    const commented = join(traecodeUserDataDirectory(home), "User", "settings.json");
    await writeFile(commented, '{\n  // kept by hand\n  "theme": "light"\n}\n');
    expect(await enableTraecodeWorkspaceMcp(home)).toBe(false);
    expect(await readFile(commented, "utf8")).toContain("// kept by hand");
  });
});

describe("the ZCode document seams", () => {
  it("turns the hook runner on, fills the output cap, and nests the events", async () => {
    const directory = await mkdtemp(join(tmpdir(), "zcode-doc-"));
    const configPath = join(directory, "config.json");
    await writeFile(
      configPath,
      JSON.stringify({ model: "glm-4.7", hooks: { maxOutputBytes: 1024, enabled: false } }),
    );
    const result = await installLifecycleHooks({
      hostId: "zcode",
      configPath,
      format: "json",
      runtimeDirectory: STAGED,
    });
    expect(result.ok).toBe(true);
    const document = JSON.parse(await readFile(configPath, "utf8")) as {
      model: string;
      hooks: { enabled: boolean; maxOutputBytes: number; events: Record<string, unknown> };
    };
    expect(document.model).toBe("glm-4.7");
    // The runner ships off: install turns it on or the lifecycle never fires.
    expect(document.hooks.enabled).toBe(true);
    // A user who tuned the cap owns it: the seed fills it only when absent.
    expect(document.hooks.maxOutputBytes).toBe(1024);
    const stop = document.hooks.events["Stop"] as { hooks: { type: string; args: string[]; timeoutMs: number }[] };
    expect(stop.hooks[0]?.args[0]).toBe(hookPathFor("stop.mjs", STAGED));
    expect(stop.hooks[0]?.timeoutMs).toBe(60_000);
  });
});

it("resolves the declared event spellings for the new hosts", () => {
  expect(registrationFor("factory-droid", "session-start").event).toBe("SessionStart");
  expect(registrationFor("traecode", "pre-prompt").event).toBe("UserPromptSubmit");
  expect(registrationFor("grok-build", "stop").event).toBe("Stop");
});
