import { describe, expect, it } from "vitest";
import { declaredEvents } from "../helpers/hooks";
import { BACKUP_SUFFIX } from "../../src/write/backup";
import { OWNERSHIP_MARKER } from "../../src/write/marker";
import { NO_HOST_CLI, runInstaller, type FakeHome, withFakeHome } from "../helpers/fake-home";

/**
 * A host config that permits comments is edited as text, so comments, trailing
 * commas and the file's own line endings survive install and uninstall. One
 * that will not parse is left byte-identical instead of being rewritten.
 */

const DECLARED_EVENT_COUNT = declaredEvents("claude-code").length;

const COMMENTED = [
  "{",
  "  // keep this comment: the operator wrote it",
  '  "model": "opus",',
  '  "hooks": {',
  '    "session-start": [',
  '      { "command": "echo foreign" },',
  "    ],",
  "  },",
  "}",
  "",
].join("\n");

const BROKEN = ["{", "  // this file was left half-written", '  "hooks": [', ""].join("\n");

function occurrences(text: string, needle: string): number {
  return text.split(needle).length - 1;
}

describe("a host config that permits comments", () => {
  it("survives install with its comments, foreign entry and formatting", async () => {
    await withFakeHome(
      { files: { ".claude/settings.json": COMMENTED }, environment: { PATH: NO_HOST_CLI } },
      async (home: FakeHome) => {
        const result = await runInstaller(["install", "claude-code"]);
        expect(result.status).toBe(0);

        const text: string = await home.read(".claude/settings.json");
        expect(text).toContain("// keep this comment: the operator wrote it");
        expect(text).toContain('"model": "opus"');
        expect(text).toContain('"command": "echo foreign"');
        // One registration per lifecycle event, and no marker anywhere else.
        expect(occurrences(text, OWNERSHIP_MARKER)).toBe(DECLARED_EVENT_COUNT);
        expect(text.endsWith("\n")).toBe(true);
      },
    );
  });

  it("keeps the file's own line endings", async () => {
    const crlf: string = COMMENTED.replace(/\n/g, "\r\n");
    await withFakeHome(
      { files: { ".claude/settings.json": crlf }, environment: { PATH: NO_HOST_CLI } },
      async (home: FakeHome) => {
        expect((await runInstaller(["install", "claude-code"])).status).toBe(0);
        const text: string = await home.read(".claude/settings.json");
        expect(text).toContain("\r\n");
        expect(text.replace(/\r\n/g, "").includes("\n")).toBe(false);
      },
    );
  });

  it("survives uninstall with its comments and foreign entry", async () => {
    await withFakeHome(
      { files: { ".claude/settings.json": COMMENTED }, environment: { PATH: NO_HOST_CLI } },
      async (home: FakeHome) => {
        expect((await runInstaller(["install", "claude-code"])).status).toBe(0);
        expect((await runInstaller(["uninstall", "claude-code"])).status).toBe(0);

        const text: string = await home.read(".claude/settings.json");
        expect(text).toContain("// keep this comment: the operator wrote it");
        expect(text).toContain('"model": "opus"');
        expect(text).toContain('"command": "echo foreign"');
        expect(occurrences(text, OWNERSHIP_MARKER)).toBe(0);
      },
    );
  });

  it("writes no competing config file beside the host's own", async () => {
    await withFakeHome(
      { files: { ".claude/settings.json": COMMENTED }, environment: { PATH: NO_HOST_CLI } },
      async (home: FakeHome) => {
        expect((await runInstaller(["install", "claude-code"])).status).toBe(0);
        const settings: string[] = (await home.listFiles()).filter((path: string) =>
          path.includes("settings"),
        );
        expect(settings.sort()).toEqual([
          ".claude/settings.json",
          `.claude/settings.json${BACKUP_SUFFIX}`,
        ]);
      },
    );
  });
});

describe("a host config that permits comments but will not parse", () => {
  it("is left byte-identical and the host is skipped", async () => {
    await withFakeHome(
      { files: { ".claude/settings.json": BROKEN }, environment: { PATH: NO_HOST_CLI } },
      async (home: FakeHome) => {
        const result = await runInstaller(["install", "claude-code"]);
        expect(result.status).not.toBe(0);
        expect(result.combined()).toContain("by hand");
        expect(await home.read(".claude/settings.json")).toBe(BROKEN);
      },
    );
  });
});
