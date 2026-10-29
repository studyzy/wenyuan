import { describe, expect, it } from "vitest";
import { hostLifecycle } from "../../src/hosts/lifecycle";
import { DSH_DEFAULT_PROFILE, DSH_MCP_CONFIG, DSH_PROFILES_DIRECTORY } from "../../src/hosts/dsh";
import { DSH_PLUGIN_PACKAGE } from "../../src/hosts/dsh-plugin";
import { MCP_HARNESS_ENV, MCP_SERVER_NAME, MCP_SERVERS_KEY } from "../../src/mcp/mechanisms";
import { MCP_SERVER_FILE_NAME, PER_USER_DIRECTORY_NAME, RUNTIME_DIRECTORY_NAME } from "../../src/runtime/layout";
import { OWNERSHIP_MARKER } from "../../src/write/marker";
import {
  runInstaller,
  withFakeHome,
  withRestrictedPath,
  writeStandInDsh,
  type FakeHome,
  type JsonDocument,
} from "../helpers/fake-home";

/**
 * DSH, driven through the real installer.
 *
 * DSH's memory wiring is the Cordis plugin `@studyzy/dsh-wenyuan-memory`, added
 * to each profile with DSH's own `dsh plugin` forwarder: installing hooks here
 * beside the plugin would recall twice per prompt and retain the transcript
 * twice. The MCP server entry and the companion skill remain this package's,
 * and the absence of hook config is the part a regression would silently
 * reintroduce.
 *
 * The `dsh` executable is a recording stand-in on PATH: the installer must go
 * through the real CLI, so what is asserted is the exact invocation it makes,
 * per profile, rather than a profile directory it would write itself.
 */

const HOST = "dsh";
const MCP = DSH_MCP_CONFIG;
const SKILL = `.dsh/skills/${OWNERSHIP_MARKER}/SKILL.md`;
const CALL_LOG = "bin/dsh-calls.log";

async function recordedCalls(home: FakeHome): Promise<string[]> {
  if (!(await home.exists(CALL_LOG))) {
    return [];
  }
  return (await home.read(CALL_LOG)).split("\n").filter((line) => line.length > 0);
}

function pluginInvocation(profile: string): string {
  return `plugin --profile ${profile} add ${DSH_PLUGIN_PACKAGE}`;
}

async function install(): Promise<void> {
  const result = await runInstaller(["install", HOST]);
  expect(result.status).toBe(0);
}

describe("installing DSH", () => {
  it("declares no lifecycle at all, because a plugin owns those seams", () => {
    // `hostLifecycle` throws for a host with no entry in the declaration; that
    // absence is the contract here.
    expect(() => hostLifecycle(HOST)).toThrow(/no lifecycle/);
  });

  it("adds the published plugin to the web profile when no profile exists yet", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
            await withRestrictedPath([await writeStandInDsh(home)], () => install());
      expect(await recordedCalls(home)).toEqual([pluginInvocation(DSH_DEFAULT_PROFILE)]);
    });
  });

  it("adds the plugin to every profile the machine already runs", async () => {
    await withFakeHome(
      {
        files: {
          [`${DSH_PROFILES_DIRECTORY}/web/package.json`]: "{}\n",
          [`${DSH_PROFILES_DIRECTORY}/headless/package.json`]: "{}\n",
          // Neither of these is a profile: a reserved name belongs to the Desktop
          // application, and a directory without a manifest is not one DSH booted.
          [`${DSH_PROFILES_DIRECTORY}/desktop/package.json`]: "{}\n",
          [`${DSH_PROFILES_DIRECTORY}/scratch/notes.txt`]: "not a profile\n",
        },
        environment: { PATH: "" },
      },
      async (home: FakeHome) => {
                await withRestrictedPath([await writeStandInDsh(home)], () => install());
        expect(await recordedCalls(home)).toEqual([
          pluginInvocation("headless"),
          pluginInvocation("web"),
        ]);
      },
    );
  });

  it("exits non-zero and names the DSH command when the plugin cannot be added", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      const bin: string = await writeStandInDsh(home, 1);
      const result = await withRestrictedPath([bin], () => runInstaller(["install", HOST]));
      expect(result.status).not.toBe(0);
      expect(result.combined()).toContain(`dsh plugin --profile ${DSH_DEFAULT_PROFILE} add ${DSH_PLUGIN_PACKAGE}`);
    });
  });

  it("writes its own MCP server map naming DSH as the caller", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
            await withRestrictedPath([await writeStandInDsh(home)], () => install());
      const document: JsonDocument = await home.readJson(MCP);
      const servers = document[MCP_SERVERS_KEY] as JsonDocument;
      expect(Object.keys(servers)).toEqual([MCP_SERVER_NAME]);
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
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
            await withRestrictedPath([await writeStandInDsh(home)], () => install());
      expect(await home.exists(".dsh/hooks/wenyuan.json")).toBe(false);
      expect(await home.exists(".dsh/config.json")).toBe(false);
      expect(await home.exists(".dsh/hooks")).toBe(false);
    });
  });

  it("copies the companion skill into the DSH skills root", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
            await withRestrictedPath([await writeStandInDsh(home)], () => install());
      expect(await home.isFile(SKILL)).toBe(true);
    });
  });

  it("keeps a foreign server-map member alone", async () => {
    await withFakeHome(
      { files: { [MCP]: `${JSON.stringify({ mcpServers: { other: { command: "x" } } }, null, 2)}\n` }, environment: { PATH: "" } },
      async (home: FakeHome) => {
                await withRestrictedPath([await writeStandInDsh(home)], () => install());
        const document: JsonDocument = await home.readJson(MCP);
        expect(Object.keys(document[MCP_SERVERS_KEY] as JsonDocument).sort()).toEqual(["other", MCP_SERVER_NAME].sort());
      },
    );
  });

  it("is idempotent: a second install leaves one server entry", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
            await withRestrictedPath([await writeStandInDsh(home)], async () => {
        await install();
        const first: string = await home.read(MCP);
        await install();
        expect(await home.read(MCP)).toBe(first);
      });
      const document: JsonDocument = await home.readJson(MCP);
      expect(Object.keys(document[MCP_SERVERS_KEY] as JsonDocument)).toEqual([MCP_SERVER_NAME]);
    });
  });

  it("uninstalls what it wrote and leaves the profile's plugin installed", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
            await withRestrictedPath([await writeStandInDsh(home)], async () => {
        await install();
        expect((await runInstaller(["uninstall", HOST])).status).toBe(0);
      });
      expect(await home.exists(MCP)).toBe(false);
      expect(await home.exists(`.dsh/skills/${OWNERSHIP_MARKER}`)).toBe(false);
      // Uninstalling never invokes DSH: profile membership is DSH's own state.
      expect(await recordedCalls(home)).toHaveLength(1);
    });
  });
});