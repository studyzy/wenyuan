import { describe, expect, it } from "vitest";
import {
  MCP_HARNESS_ENV,
  MCP_SERVER_NAME,
  MCP_SERVERS_KEY,
  inspectMcpServer,
  mcpServerEntry,
  registerMcpServer,
  removeMcpServer,
} from "../../src/mcp/register";
import { OWNERSHIP_MARKER } from "../../src/write/marker";
import { withFakeHome, type FakeHome } from "../helpers/fake-home";

/**
 * MCP registration: the entry's shape, the caller-identifying variable, and
 * the ownership guard over a same-named entry this package did not create.
 */

const hostId = "claude-code";
const stagedServer = `/home/tester/.wenyuan/${OWNERSHIP_MARKER}/mcp-server.mjs`;
const configPath = (home: FakeHome): string => home.path(".claude.json");

const target = (home: FakeHome) => ({
  configPath: configPath(home),
  format: "json" as const,
  hostId,
});

describe("the registration's shape", () => {
  it("names the calling host through the caller-identifying variable", () => {
    const entry = mcpServerEntry(stagedServer, hostId);
    expect(entry.env?.[MCP_HARNESS_ENV]).toBe(hostId);
    expect(entry.args).toContain(stagedServer);
  });

  it("carries the marker in the command path it points at", () => {
    const entry = mcpServerEntry(stagedServer, hostId);
    expect(JSON.stringify(entry)).toContain(OWNERSHIP_MARKER);
  });
});

describe("installing into a host's MCP config", () => {
  it("creates the file with one server entry when there is none", async () => {
    await withFakeHome({}, async (home) => {
      const result = await registerMcpServer({ ...target(home), serverPath: stagedServer });
      expect(result.ok).toBe(true);
      expect(result.written).toBe(true);
      const document = JSON.parse(await home.read(".claude.json")) as {
        mcpServers: Record<string, { env?: Record<string, string> }>;
      };
      expect(Object.keys(document[MCP_SERVERS_KEY])).toEqual([MCP_SERVER_NAME]);
      expect(document.mcpServers[MCP_SERVER_NAME]?.env?.[MCP_HARNESS_ENV]).toBe(hostId);
    });
  });

  it("is idempotent: a second run leaves exactly one entry", async () => {
    await withFakeHome({}, async (home) => {
      await registerMcpServer({ ...target(home), serverPath: stagedServer });
      const after: string = await home.read(".claude.json");
      await registerMcpServer({ ...target(home), serverPath: stagedServer });
      expect(await home.read(".claude.json")).toBe(after);
      expect(await inspectMcpServer(target(home))).toBe("owned");
    });
  });

  it("keeps foreign servers alongside its own", async () => {
    await withFakeHome({}, async (home) => {
      await home.write(
        ".claude.json",
        '{\n  "mcpServers": {\n    "other-a": { "command": "a" },\n    "other-b": { "command": "b" }\n  }\n}\n',
      );
      const result = await registerMcpServer({ ...target(home), serverPath: stagedServer });
      expect(result.ok).toBe(true);
      const document = JSON.parse(await home.read(".claude.json")) as {
        mcpServers: Record<string, unknown>;
      };
      expect(Object.keys(document.mcpServers)).toContain(MCP_SERVER_NAME);
      expect(Object.keys(document.mcpServers).sort()).toEqual([
        "other-a",
        "other-b",
        "wenyuan-memory",
      ]);
    });
  });

  it("reports a conflict and writes nothing when the name is taken by a foreign entry", async () => {
    await withFakeHome({}, async (home) => {
      const before: string =
        '{\n  "mcpServers": {\n    "wenyuan-memory": { "command": "someone-elses" }\n  }\n}\n';
      await home.write(".claude.json", before);
      const result = await registerMcpServer({ ...target(home), serverPath: stagedServer });
      expect(result.ok).toBe(false);
      expect(result.written).toBe(false);
      expect(result.messages.join(" ")).toContain("wenyuan-memory");
      expect(await home.read(".claude.json")).toBe(before);
      expect(await inspectMcpServer(target(home))).toBe("foreign");
    });
  });
});

describe("removing the registration", () => {
  it("removes its own entry and leaves a foreign one in place", async () => {
    await withFakeHome({}, async (home) => {
      await home.write(
        ".claude.json",
        '{\n  "mcpServers": {\n    "other-a": { "command": "a" }\n  }\n}\n',
      );
      await registerMcpServer({ ...target(home), serverPath: stagedServer });
      const result = await removeMcpServer(target(home));
      expect(result.ok).toBe(true);
      const document = JSON.parse(await home.read(".claude.json")) as {
        mcpServers: Record<string, unknown>;
      };
      expect(Object.keys(document.mcpServers)).toEqual(["other-a"]);
    });
  });

  it("deletes the file the removal emptied rather than leaving a husk", async () => {
    await withFakeHome({}, async (home) => {
      await registerMcpServer({ ...target(home), serverPath: stagedServer });
      expect(await home.exists(".claude.json")).toBe(true);
      await removeMcpServer(target(home));
      expect(await home.exists(".claude.json")).toBe(false);
    });
  });

  it("leaves a foreign same-named entry untouched", async () => {
    await withFakeHome({}, async (home) => {
      const before: string =
        '{\n  "mcpServers": {\n    "wenyuan-memory": { "command": "someone-elses" }\n  }\n}\n';
      await home.write(".claude.json", before);
      const result = await removeMcpServer(target(home));
      expect(result.ok).toBe(true);
      expect(await home.read(".claude.json")).toBe(before);
    });
  });

  it("writes a backup of a pre-existing file before the first change", async () => {
    await withFakeHome({}, async (home) => {
      await home.write(".claude.json", '{\n  "theme": "dark"\n}\n');
      await registerMcpServer({ ...target(home), serverPath: stagedServer });
      const backup: string = `${configPath(home)}.${OWNERSHIP_MARKER}.bak`;
      const { readFile } = await import("node:fs/promises");
      expect(await readFile(backup, "utf8")).toBe('{\n  "theme": "dark"\n}\n');
    });
  });
});

describe("a host config that permits comments", () => {
  const jsoncTarget = (home: FakeHome) => ({
    configPath: home.path(".codebuddy", "mcp.json"),
    format: "jsonc" as const,
    hostId: "codebuddy",
  });

  it("adds the entry and keeps the comments", async () => {
    await withFakeHome({}, async (home) => {
      await home.write(
        ".codebuddy/mcp.json",
        '{\n  // my servers\n  "mcpServers": {\n    "other-a": { "command": "a" }\n  }\n}\n',
      );
      const result = await registerMcpServer({ ...jsoncTarget(home), serverPath: stagedServer });
      expect(result.ok).toBe(true);
      const text: string = await home.read(".codebuddy/mcp.json");
      expect(text).toContain("// my servers");
      expect(text).toContain(MCP_HARNESS_ENV);
      expect(text).toContain("codebuddy");
      expect(text).toContain("other-a");
    });
  });

  it("removes only its own entry and leaves the comments behind", async () => {
    await withFakeHome({}, async (home) => {
      await home.write(
        ".codebuddy/mcp.json",
        '{\n  // my servers\n  "mcpServers": {\n    "other-a": { "command": "a" }\n  }\n}\n',
      );
      await registerMcpServer({ ...jsoncTarget(home), serverPath: stagedServer });
      await removeMcpServer(jsoncTarget(home));
      const text: string = await home.read(".codebuddy/mcp.json");
      expect(text).toContain("// my servers");
      expect(text).toContain("other-a");
      expect(text).not.toContain(OWNERSHIP_MARKER);
    });
  });
});
