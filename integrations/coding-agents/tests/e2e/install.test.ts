import { mkdir } from "node:fs/promises";
import { describe, expect, it } from "vitest";
import { MCP_SERVERS_KEY } from "../../src/mcp/register";
import { OWNERSHIP_MARKER } from "../../src/write/marker";
import { declaredEvents, ownEntryCount } from "../helpers/hooks";
import { runInstaller, withFakeHome, type FakeHome, type JsonDocument } from "../helpers/fake-home";

/**
 * The install command's opt-in flags, and what happens when one host in a
 * multi-host invocation cannot be wired: the healthy hosts are still wired, the
 * blocked one writes nothing, and every host that was not installed is named in
 * a trailing summary.
 */

const CONFIG = ".claude/settings.json";
const MCP = ".claude.json";
const SKILL = `.claude/skills/${OWNERSHIP_MARKER}/claude-code/SKILL.md`;
const WORKSPACE_KEY = "enableAllProjectMcpServers";

describe("the conversation-backfill opt-in flag", () => {
  it("attempts the backfill only when the flag is passed, and wires identically", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      const without = await runInstaller(["install", "claude-code"]);
      expect(without.status).toBe(0);
      expect(without.combined()).not.toContain("backfill");
      const configAfterPlain: string = await home.read(CONFIG);
      const mcpAfterPlain: string = await home.read(MCP);

      const withFlag = await runInstaller(["install", "claude-code", "--import-conversations"]);
      expect(withFlag.status).toBe(0);
      expect(withFlag.combined()).toContain("backfill");

      // Identical wiring, compared semantically: a re-run may reformat the
      // entry it replaced but must not change what the host reads.
      expect(JSON.parse(await home.read(CONFIG))).toEqual(JSON.parse(configAfterPlain));
      expect(JSON.parse(await home.read(MCP))).toEqual(JSON.parse(mcpAfterPlain));
      expect(await home.isFile(SKILL)).toBe(true);
    });
  });

  it("never blocks on input in a non-interactive run", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async () => {
      const result = await runInstaller(["install", "claude-code", "--import-conversations"]);
      expect(result.status).toBe(0);
      expect(result.combined()).not.toContain("[y/N]");
    });
  });
});

describe("the workspace-MCP opt-in flag", () => {
  it("refuses to flip the global setting without the flag", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      const result = await runInstaller(["install", "claude-code"]);
      expect(result.status).toBe(0);
      expect(result.combined()).toContain(WORKSPACE_KEY);
      expect(await home.read(CONFIG)).not.toContain(WORKSPACE_KEY);
      expect(await home.isFile(SKILL)).toBe(true);
    });
  });

  it("flips the global setting with the flag", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      const result = await runInstaller(["install", "claude-code", "--enable-workspace-mcp"]);
      expect(result.status).toBe(0);
      expect(await home.read(CONFIG)).toContain(`"${WORKSPACE_KEY}": true`);
    });
  });

  it("accepts the flag in the equals-joined spelling too", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      const result = await runInstaller(["install", "claude-code", "--enable-workspace-mcp=true"]);
      expect(result.status).toBe(0);
      expect(await home.read(CONFIG)).toContain(`"${WORKSPACE_KEY}": true`);
    });
  });
});

describe("a host whose preflight check fails", () => {
  it("is skipped while the healthy host in the same invocation is wired", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      // A directory wearing the host config's name can never be written.
      await mkdir(home.path(".claude/settings.json"), { recursive: true });

      const result = await runInstaller(["install", "claude-code", "codex"]);
      expect(result.status).not.toBe(0);
      expect(result.combined()).toContain("not installed:");
      expect(result.combined()).toContain("claude-code");

      expect(await home.isFile(".codex/settings.json")).toBe(true);
      const codex: JsonDocument = await home.readJson(".codex/settings.json");
      for (const event of declaredEvents("codex")) {
        expect(ownEntryCount(codex, event)).toBe(1);
      }
      expect(Object.keys((await home.readJson(".codex/mcp.json"))[MCP_SERVERS_KEY] as JsonDocument)).toEqual([
        OWNERSHIP_MARKER,
      ]);

      expect(await home.isFile(".claude/settings.json")).toBe(false);
      expect(await home.exists(".claude.json")).toBe(false);
    });
  });
});
