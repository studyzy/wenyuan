import { pathToFileURL } from "node:url";
import { describe, expect, it } from "vitest";
import { hostLifecycle } from "../../src/hosts/lifecycle";
import {
  DSH_BLOCK_END,
  DSH_BLOCK_START,
  DSH_EMPTY_LIST,
  DSH_MCP_CONFIG,
  DSH_PATCH_FILE,
} from "../../src/hosts/dsh";
import { MCP_HARNESS_ENV, MCP_SERVER_NAME, MCP_SERVERS_KEY } from "../../src/mcp/mechanisms";
import { MCP_SERVER_FILE_NAME, PER_USER_DIRECTORY_NAME, RUNTIME_DIRECTORY_NAME } from "../../src/runtime/layout";
import { OWNERSHIP_MARKER } from "../../src/write/marker";
import { NO_HOST_CLI, runInstaller, type FakeHome, type JsonDocument, withFakeHome } from "../helpers/fake-home";

/**
 * DSH, driven through the real installer.
 *
 * DSH's memory wiring is the Cordis plugin this package ships, registered as a
 * row in the HOME-level patch layer ($DSH_HOME/cordis.patch.yml) that every
 * profile composes: installing hooks beside the plugin would recall twice per
 * prompt and retain the transcript twice. The row needs no dsh executable and
 * no network — it loads the dist entry already staged on this machine — so
 * what is asserted is the patch file itself. The MCP server entry and the
 * companion skill remain this package's, and the absence of hook config is the
 * part a regression would silently reintroduce.
 */

const HOST = "dsh";
const MCP = DSH_MCP_CONFIG;
const PATCH = `.dsh/${DSH_PATCH_FILE}`;
const SKILL = `.dsh/skills/${OWNERSHIP_MARKER}/SKILL.md`;

async function install(): Promise<void> {
  const result = await runInstaller(["install", HOST]);
  expect(result.status).toBe(0);
}

function stagedPluginUrl(home: FakeHome): string {
  return pathToFileURL(
    home.path(PER_USER_DIRECTORY_NAME, RUNTIME_DIRECTORY_NAME, "plugins", "dsh.mjs"),
  ).href;
}

describe("installing DSH", () => {
  it("declares no lifecycle at all, because a plugin owns those seams", () => {
    // hostLifecycle throws for a host with no entry in the declaration; that
    // absence is the contract here.
    expect(() => hostLifecycle(HOST)).toThrow(/no lifecycle/);
  });

  it("registers the plugin row in the home-level patch layer", async () => {
    await withFakeHome({ environment: { PATH: NO_HOST_CLI } }, async (home: FakeHome) => {
      await install();
      const patch: string = await home.read(PATCH);
      expect(patch).toContain(DSH_BLOCK_START);
      expect(patch).toContain("- insert:");
      expect(patch).toContain("id: wenyuan");
      expect(patch).toContain(JSON.stringify(stagedPluginUrl(home)));
      expect(patch).toContain(DSH_BLOCK_END);
    });
  });

  it("replaces its own block on re-install and keeps user content", async () => {
    await withFakeHome(
      { files: { [PATCH]: "- insert:\n    - id: mine\n      name: file:///mine.js\n" }, environment: { PATH: NO_HOST_CLI } },
      async (home: FakeHome) => {
        await install();
        const first: string = await home.read(PATCH);
        expect(first).toContain("id: mine");
        expect(first).toContain("id: wenyuan");
        await install();
        const second: string = await home.read(PATCH);
        expect(second).toContain("id: mine");
        expect(second.match(new RegExp(DSH_BLOCK_START, "g"))).toHaveLength(1);
      },
    );
  });

  it("points the row at a plugin file staging actually creates", async () => {
    // DSH resolves the row's name as an ES module specifier, so a file that was
    // never staged is not an error the host reports — the plugin is skipped and
    // the session runs with no memory at all.
    await withFakeHome({ environment: { PATH: NO_HOST_CLI } }, async (home: FakeHome) => {
      await install();
      expect(
        await home.isFile(`${PER_USER_DIRECTORY_NAME}/${RUNTIME_DIRECTORY_NAME}/plugins/dsh.mjs`),
      ).toBe(true);
    });
  });

  it("writes its own MCP server map naming DSH as the caller", async () => {
    await withFakeHome({ environment: { PATH: NO_HOST_CLI } }, async (home: FakeHome) => {
      await install();
      const document: JsonDocument = await home.readJson(MCP);
      const servers = document[MCP_SERVERS_KEY] as JsonDocument;
      const entry = servers[MCP_SERVER_NAME] as JsonDocument;
      expect(entry["command"]).toBe("node");
      expect((entry["env"] as JsonDocument)[MCP_HARNESS_ENV]).toBe(HOST);
      const args: string[] = entry["args"] as string[];
      expect(args[0]).toContain(PER_USER_DIRECTORY_NAME);
      expect(args[0]).toContain(MCP_SERVER_FILE_NAME);
      expect(args[0]).not.toContain(home.packageRoot);
      expect(await home.isFile(`${PER_USER_DIRECTORY_NAME}/${RUNTIME_DIRECTORY_NAME}/${MCP_SERVER_FILE_NAME}`)).toBe(true);
    });
  });

  it("writes no hook config, so nothing recalls or retains a second time", async () => {
    await withFakeHome({ environment: { PATH: NO_HOST_CLI } }, async (home: FakeHome) => {
      await install();
      expect(await home.exists(".dsh/hooks/wenyuan.json")).toBe(false);
      expect(await home.exists(".dsh/config.json")).toBe(false);
      expect(await home.exists(".dsh/hooks")).toBe(false);
    });
  });

  it("copies the companion skill into the DSH skills root", async () => {
    await withFakeHome({ environment: { PATH: NO_HOST_CLI } }, async (home: FakeHome) => {
      await install();
      expect(await home.isFile(SKILL)).toBe(true);
    });
  });

  it("keeps a foreign server-map member alone", async () => {
    await withFakeHome(
      { files: { [MCP]: `${JSON.stringify({ mcpServers: { other: { command: "x" } } }, null, 2)}\n` }, environment: { PATH: NO_HOST_CLI } },
      async (home: FakeHome) => {
        await install();
        const document: JsonDocument = await home.readJson(MCP);
        expect(Object.keys(document[MCP_SERVERS_KEY] as JsonDocument).sort()).toEqual(["other", MCP_SERVER_NAME].sort());
      },
    );
  });

  it("is idempotent: a second install leaves one server entry", async () => {
    await withFakeHome({ environment: { PATH: NO_HOST_CLI } }, async (home: FakeHome) => {
      await install();
      const first: string = await home.read(MCP);
      await install();
      expect(await home.read(MCP)).toBe(first);
      const document: JsonDocument = await home.readJson(MCP);
      expect(Object.keys(document[MCP_SERVERS_KEY] as JsonDocument)).toEqual([MCP_SERVER_NAME]);
    });
  });

  it("uninstall strips the block, leaving an empty list when nothing else remains", async () => {
    await withFakeHome({ environment: { PATH: NO_HOST_CLI } }, async (home: FakeHome) => {
      await install();
      expect((await runInstaller(["uninstall", HOST])).status).toBe(0);
      expect(await home.read(PATCH)).toBe(DSH_EMPTY_LIST + "\n");
      expect(await home.exists(MCP)).toBe(false);
      expect(await home.exists(`.dsh/skills/${OWNERSHIP_MARKER}`)).toBe(false);
    });
  });

  it("uninstall keeps user content and removes only its own block", async () => {
    await withFakeHome(
      { files: { [PATCH]: "- insert:\n    - id: mine\n      name: file:///mine.js\n" }, environment: { PATH: NO_HOST_CLI } },
      async (home: FakeHome) => {
        await install();
        expect((await runInstaller(["uninstall", HOST])).status).toBe(0);
        const patch: string = await home.read(PATCH);
        expect(patch).toContain("id: mine");
        expect(patch).not.toContain("id: wenyuan");
      },
    );
  });
});
