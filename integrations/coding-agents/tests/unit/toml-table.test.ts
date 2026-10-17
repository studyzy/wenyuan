import { describe, expect, it } from "vitest";
import {
  applyTomlEdit,
  registerTomlTable,
  removeTomlTable,
  stripOurTable,
} from "../../src/mcp/toml-table";
import {
  MCP_HARNESS_ENV,
  MCP_LAUNCHER_COMMAND,
  MCP_SERVER_NAME,
  MCP_TOML_CONTAINER_KEY,
} from "../../src/mcp/mechanisms";
import { BACKUP_SUFFIX } from "../../src/write/backup";
import { withFakeHome, type FakeHome } from "../helpers/fake-home";

/**
 * The TOML host's block edit: strip any previous table of ours (with its
 * sub-tables), append the rewritten block, leave everything else alone, and
 * write only when the text differs.
 */

const hostId = "codex";
const staged = "/home/tester/.wenyuan/wenyuan-memory/mcp-server.mjs";
const CONFIG = ".codex/config.toml";

const UNRELATED = 'model = "opus"\n\n[features]\nreasoning = true\n';

describe("stripping the previous table of ours", () => {
  it("removes our table and its sub-tables, keeping the rest", () => {
    const source = [
      'model = "opus"',
      "",
      `[${MCP_TOML_CONTAINER_KEY}.${MCP_SERVER_NAME}]`,
      'command = "node"',
      "",
      `[${MCP_TOML_CONTAINER_KEY}.${MCP_SERVER_NAME}.extra]`,
      'note = "stale"',
      "",
      "[features]",
      "reasoning = true",
      "",
    ].join("\n");
    const stripped = stripOurTable(source, MCP_TOML_CONTAINER_KEY);
    expect(stripped).toContain('model = "opus"');
    expect(stripped).toContain("[features]");
    expect(stripped).not.toContain(MCP_SERVER_NAME);
    expect(stripped).not.toContain("extra");
  });

  it("leaves a document with no table of ours unchanged", () => {
    expect(stripOurTable(UNRELATED, MCP_TOML_CONTAINER_KEY)).toBe(UNRELATED);
  });
});

describe("rendering and applying the block", () => {
  it("appends one table with the launcher, argv and inline environment", () => {
    const text = applyTomlEdit(UNRELATED, { serverPath: staged, hostId });
    expect(text).toContain(`[${MCP_TOML_CONTAINER_KEY}.${MCP_SERVER_NAME}]`);
    expect(text).toContain(`command = "${MCP_LAUNCHER_COMMAND}"`);
    expect(text).toContain(`args = ["${staged}"]`);
    expect(text).toContain(`${MCP_HARNESS_ENV} = "${hostId}"`);
    // The unrelated section survives verbatim.
    expect(text).toContain("reasoning = true");
  });

  it("rewrites rather than duplicates when one is already present", () => {
    const first = applyTomlEdit(UNRELATED, { serverPath: staged, hostId });
    const moved = applyTomlEdit(first, { serverPath: "/new/location/mcp-server.mjs", hostId });
    expect(moved.match(new RegExp(`\\[${MCP_TOML_CONTAINER_KEY}\\.${MCP_SERVER_NAME}\\]`, "g"))).toHaveLength(1);
    expect(moved).toContain("/new/location/mcp-server.mjs");
    expect(moved).not.toContain(staged);
  });
});

describe("writing the TOML config", () => {
  it("creates the file and a missing parent directory", async () => {
    await withFakeHome({}, async (home: FakeHome) => {
      const result = await registerTomlTable({ home: home.home, serverPath: staged, hostId });
      expect(result.ok).toBe(true);
      expect(result.written).toBe(true);
      const text = await home.read(CONFIG);
      expect(text).toContain(`[${MCP_TOML_CONTAINER_KEY}.${MCP_SERVER_NAME}]`);
    });
  });

  it("is a no-op on a second run", async () => {
    await withFakeHome({}, async (home: FakeHome) => {
      await registerTomlTable({ home: home.home, serverPath: staged, hostId });
      const after = await home.read(CONFIG);
      const second = await registerTomlTable({ home: home.home, serverPath: staged, hostId });
      expect(second.written).toBe(false);
      expect(await home.read(CONFIG)).toBe(after);
    });
  });

  it("writes the backup once and never overwrites it", async () => {
    await withFakeHome({ files: { [CONFIG]: UNRELATED } }, async (home: FakeHome) => {
      await registerTomlTable({ home: home.home, serverPath: staged, hostId });
      expect(await home.read(`${CONFIG}${BACKUP_SUFFIX}`)).toBe(UNRELATED);
      await registerTomlTable({ home: home.home, serverPath: "/moved/mcp-server.mjs", hostId });
      expect(await home.read(`${CONFIG}${BACKUP_SUFFIX}`)).toBe(UNRELATED);
    });
  });

  it("removes our table and deletes a file it emptied", async () => {
    await withFakeHome({}, async (home: FakeHome) => {
      await registerTomlTable({ home: home.home, serverPath: staged, hostId });
      expect(await home.exists(CONFIG)).toBe(true);
      await removeTomlTable({ home: home.home, serverPath: staged, hostId });
      expect(await home.exists(CONFIG)).toBe(false);
    });
  });

  it("keeps unrelated content on removal", async () => {
    await withFakeHome({ files: { [CONFIG]: UNRELATED } }, async (home: FakeHome) => {
      await registerTomlTable({ home: home.home, serverPath: staged, hostId });
      await removeTomlTable({ home: home.home, serverPath: staged, hostId });
      const text = await home.read(CONFIG);
      expect(text).toContain('model = "opus"');
      expect(text).not.toContain(MCP_SERVER_NAME);
    });
  });
});