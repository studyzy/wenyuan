import { chmod, mkdir, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import {
  addArguments,
  registerCommandLine,
  removeArguments,
  removeCommandLine,
} from "../../src/mcp/command-line";
import {
  MCP_HARNESS_ENV,
  MCP_LAUNCHER_COMMAND,
  MCP_SERVER_NAME,
} from "../../src/mcp/mechanisms";
import { OWNERSHIP_MARKER } from "../../src/write/marker";
import { withFakeHome, type FakeHome } from "../helpers/fake-home";

/**
 * The command-line host's mechanism: the argv the add command receives (with no
 * shell involved), the remove-then-add idempotency, and the manual fallback
 * when the host interface is unusable.
 */

const hostId = "claude-code";
const staged = "/home/tester/.wenyuan/wenyuan-memory/mcp-server.mjs";

/**
 * A stand-in host interface: a Node script that records each invocation's argv
 * to a log file, so the test asserts the argument vector the package passed
 * rather than a field shape inside a store this package does not own.
 */
async function fakeInterface(home: FakeHome, behavior: "ok" | "fail"): Promise<string> {
  const path = join(home.home, "bin", "host-cli.mjs");
  await mkdir(join(home.home, "bin"), { recursive: true });
  const script = [
    "#!/usr/bin/env node",
    'import { appendFileSync } from "node:fs";',
    'const log = process.env.HOST_CLI_LOG;',
    "if (log) appendFileSync(log, JSON.stringify(process.argv.slice(2)) + \"\\n\");",
    behavior === "fail" ? "process.exit(1);" : "process.exit(0);",
    "",
  ].join("\n");
  await writeFile(path, script);
  await chmod(path, 0o755);
  return path;
}

async function logOf(home: FakeHome): Promise<string[][]> {
  const text = await home.read("host-cli.log");
  return text
    .split("\n")
    .filter((line) => line.trim() !== "")
    .map((line) => JSON.parse(line) as string[]);
}

describe("the argv the host interface receives", () => {
  it("names the server, the launcher and the staged server path with no shell", () => {
    const argv = addArguments({ executable: "claude", serverPath: staged, hostId });
    expect(argv).toContain(MCP_SERVER_NAME);
    expect(argv).toContain(MCP_LAUNCHER_COMMAND);
    expect(argv).toContain(staged);
    // The harness value travels as an environment flag, not interpolated text.
    const envIndex = argv.indexOf(`${MCP_HARNESS_ENV}=${hostId}`);
    expect(envIndex).toBeGreaterThanOrEqual(0);
  });

  it("removes the same-named entry with the remove subcommand", () => {
    const argv = removeArguments({ executable: "claude", serverPath: staged, hostId });
    expect(argv).toContain(MCP_SERVER_NAME);
    expect(argv).toContain("remove");
  });
});

describe("registering through the host interface", () => {
  it("removes then adds, so a re-install repoints rather than fails", async () => {
    await withFakeHome({}, async (home: FakeHome) => {
      const executable = await fakeInterface(home, "ok");
      process.env.HOST_CLI_LOG = home.path("host-cli.log");
      try {
        const result = await registerCommandLine({ executable, serverPath: staged, hostId });
        expect(result.ok).toBe(true);
        const log = await logOf(home);
        expect(log).toHaveLength(2);
        expect(log[0]).toContain("remove");
        expect(log[1]).toContain("add");
        expect(log[1]).toContain(staged);
      } finally {
        delete process.env.HOST_CLI_LOG;
      }
    });
  });
});

describe("a host interface that is absent or refuses", () => {
  it("reports the manual line and completes without a failure escaping", async () => {
    await withFakeHome({}, async (home: FakeHome) => {
      const result = await registerCommandLine({
        executable: join(home.home, "bin", "does-not-exist"),
        serverPath: staged,
        hostId,
      });
      expect(result.ok).toBe(true);
      expect(result.written).toBe(false);
      const message = result.messages.join("\n");
      expect(message).toContain(MCP_LAUNCHER_COMMAND);
      expect(message).toContain(staged);
    });
  });

  it("falls back when the interface exits non-zero", async () => {
    await withFakeHome({}, async (home: FakeHome) => {
      const executable = await fakeInterface(home, "fail");
      const result = await registerCommandLine({ executable, serverPath: staged, hostId });
      expect(result.ok).toBe(true);
      expect(result.written).toBe(false);
      expect(result.messages.join("\n")).toContain(staged);
    });
  });

  it("writes no file of ours when the interface is unusable", async () => {
    await withFakeHome({}, async (home: FakeHome) => {
      await registerCommandLine({
        executable: join(home.home, "bin", "does-not-exist"),
        serverPath: staged,
        hostId,
      });
      const files = await home.listFiles();
      for (const file of files) {
        expect(file).not.toContain(".claude.json");
        expect(file).not.toContain("mcp.json");
      }
    });
  });
});

describe("uninstalling through the host interface", () => {
  it("issues a remove and reports the manual line on fallback", async () => {
    await withFakeHome({}, async (home: FakeHome) => {
      const executable = await fakeInterface(home, "ok");
      process.env.HOST_CLI_LOG = home.path("host-cli.log");
      try {
        const result = await removeCommandLine({ executable, serverPath: staged, hostId });
        expect(result.ok).toBe(true);
        const log = await logOf(home);
        expect(log).toHaveLength(1);
        expect(log[0]).toContain("remove");
        expect(log[0]).toContain(MCP_SERVER_NAME);
      } finally {
        delete process.env.HOST_CLI_LOG;
      }
      // The manual line names the package's marker so the operator can follow it.
      const fallback = await removeCommandLine({
        executable: join(home.home, "bin", "does-not-exist"),
        serverPath: staged,
        hostId,
      });
      expect(fallback.ok).toBe(true);
      expect(fallback.messages.join("\n")).toContain(OWNERSHIP_MARKER);
    });
  });
});