import { describe, expect, it } from "vitest";
import { HOSTS, hostNames } from "../../src/hosts/registry";
import type { HostContribution } from "../../src/hosts/types";
import { ALL_HOSTS_KEYWORD } from "../../src/hosts/select";
import { NO_HOST_CLI, runInstaller, type FakeHome, withFakeHome, withRestrictedPath } from "../helpers/fake-home";



/**
 * Host selection: the all-hosts keyword wires every detected host, explicit
 * names wire exactly those and bypass detection, and both failures — an unknown
 * name and no selection at all — happen before anything is written.
 */

const APPROVED: string[] = ["claude-code", "codex", "codebuddy", "cursor-cli", "copilot-cli", "dsh"];

const CONFIG_FILES: Record<string, string> = {
  "claude-code": ".claude/settings.json",
  codex: ".codex/hooks.json",
  codebuddy: ".codebuddy/settings.json",
  "cursor-cli": ".cursor/hooks.json",
  "copilot-cli": ".copilot/hooks/hooks.json",
  // DSH writes no hook config; its install still wires the MCP server map and
  // the companion skill, which is what proves it was accepted.
  dsh: ".dsh/mcp.json",
};

/**
 * Two hosts detected by their config directories, with no host CLI reachable.
 *
 * An empty PATH is what makes detection fall back to the directories, and it is
 * safe here because both hosts are wired through files: the TOML host writes
 * its own table and needs no CLI of its own.
 */
const TWO_DETECTED = {
  files: { ".claude/settings.json": "{}\n", ".codex/hooks.json": "{}\n" },
  // The suite's stand-in PATH: exactly the host CLIs, nothing else, so
  // detection is decided by the config directories above and the wiring by the
  // stand-ins that answer.
  environment: { PATH: NO_HOST_CLI },
};

function hostListLine(usage: string): string[] {
  const line: string | undefined = usage
    .split("\n")
    .find((candidate: string) => candidate.startsWith("Hosts: "));
  if (line === undefined) {
    throw new Error("the usage text carries no host list line");
  }
  return line.slice("Hosts: ".length).split(", ");
}

describe("an unrecognized host name", () => {
  it("is quoted, lists the accepted ones and writes nothing", async () => {
    await withFakeHome(TWO_DETECTED, async (home: FakeHome) => {
      const result = await runInstaller(["install", "cursor"]);
      expect(result.status).not.toBe(0);
      expect(result.combined()).toContain('"cursor"');
      for (const name of APPROVED) {
        expect(result.combined()).toContain(name);
      }
      // Nothing at all was written: staging and wiring both come after
      // validation, and no host CLI was invoked.
      expect(await home.list()).toEqual([".claude", ".claude/settings.json", ".codex", ".codex/hooks.json"]);
    });
  });

  it("writes nothing for the hosts that were named alongside it", async () => {
    await withFakeHome({ environment: { PATH: NO_HOST_CLI } }, async (home: FakeHome) => {
      const result = await runInstaller(["install", "claude-code", "windsurf"]);
      expect(result.status).not.toBe(0);
      expect(result.combined()).toContain('"windsurf"');
      expect(await home.list()).toEqual([]);
    });
  });
});

describe("no host selection at all", () => {
  it("explains the choice, names the all-hosts keyword and writes nothing", async () => {
    await withFakeHome(TWO_DETECTED, async (home: FakeHome) => {
      const result = await runInstaller(["install"]);
      expect(result.status).not.toBe(0);
      expect(result.combined()).toContain(ALL_HOSTS_KEYWORD);
      expect(await home.list()).toEqual([".claude", ".claude/settings.json", ".codex", ".codex/hooks.json"]);
    });
  });

  it("says the same for uninstall", async () => {
    await withFakeHome(TWO_DETECTED, async () => {
      const result = await runInstaller(["uninstall"]);
      expect(result.status).not.toBe(0);
      expect(result.combined()).toContain(ALL_HOSTS_KEYWORD);
    });
  });
});

describe("the all-hosts keyword", () => {
  it("wires every detected host and prints which ones were detected", async () => {
    await withFakeHome(TWO_DETECTED, async (home: FakeHome) => {
      const result = await runInstaller(["install", ALL_HOSTS_KEYWORD]);
      expect(result.status).toBe(0);
      expect(result.combined()).toContain("claude-code");
      expect(result.combined()).toContain("codex");
      expect(await home.isFile(".claude/settings.json")).toBe(true);
      expect(await home.isFile(".codex/hooks.json")).toBe(true);

    });
  });

  it("asks for an explicit name when nothing is detected", async () => {
    await withFakeHome(
      { environment: { PATH: "" } },
      async (home: FakeHome) => {
        const result = await runInstaller(["install", ALL_HOSTS_KEYWORD]);
        expect(result.status).not.toBe(0);
        expect(result.combined()).toContain("explicitly");
        expect(await home.list()).toEqual([]);
      },
    );
  });
});

describe("an explicit host name", () => {
  it("bypasses detection and leaves the other host's config absent", async () => {
    await withFakeHome(TWO_DETECTED, async (home: FakeHome) => {
      const result = await runInstaller(["install", "codebuddy"]);
      expect(result.status).toBe(0);
      expect(await home.isFile(".codebuddy/settings.json")).toBe(true);
      expect(await home.read(".claude/settings.json")).toBe("{}\n");
      expect(await home.read(".codex/hooks.json")).toBe("{}\n");
    });
  });
});

describe("the registry", () => {
  it("holds exactly the registered hosts and no others", () => {
    expect(HOSTS).toHaveLength(APPROVED.length);
    expect(hostNames()).toEqual(APPROVED);
  });

  it.each([...HOSTS.map((host: HostContribution) => host.id)])("accepts %s", async (hostId: string) => {
    // Every host is driven through its own CLI or config file, so the fixture's
    // stand-ins have to be the whole PATH.
    await withFakeHome({ environment: { PATH: NO_HOST_CLI } }, async (home: FakeHome) => {
      const status: number = await withRestrictedPath([process.env.PATH ?? ""], async () =>
        (await runInstaller(["install", hostId])).status);
      expect(status).toBe(0);
      // DSH writes no hook config: its MCP server map is what proves it.
      expect(await home.isFile(CONFIG_FILES[hostId] ?? "")).toBe(true);
    });
  });

  it.each(["cursor", "windsurf", "claude"])("rejects %s", async (hostId: string) => {
    await withFakeHome({ environment: { PATH: NO_HOST_CLI } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", hostId])).status).not.toBe(0);
      expect(await home.list()).toEqual([]);
    });
  });

  it("lists exactly the same hosts in the usage text", async () => {
    await withFakeHome({ environment: { PATH: NO_HOST_CLI } }, async () => {
      const result = await runInstaller([]);
      expect(result.status).toBe(0);
      expect(hostListLine(result.stdout)).toEqual(APPROVED);
    });
  });
});
