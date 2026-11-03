import { describe, expect, it } from "vitest";
import {
  HOSTS,
  detectHosts,
  findHost,
  hostIsDetected,
  hostNames,
  isHostId,
} from "../../src/hosts/registry";
import { ALL_HOSTS_KEYWORD, selectHosts } from "../../src/hosts/select";
import { withFakeHome } from "../helpers/fake-home";

/**
 * The registry holds exactly the registered hosts, and selection resolves
 * the all-hosts keyword or explicit names before anything is written.
 */

const APPROVED = [
  "claude-code",
  "codex",
  "codebuddy",
  "workbuddy",
  "cursor-cli",
  "copilot-cli",
  "dsh",
  "antigravity-cli",
  "devin-cli",
  "grok-build",
  "qwen-code",
  "factory-droid",
  "zcode",
  "traecode",
  "kimi",
  "dcode",
];

/** Hosts whose memory wiring is a plugin of the host's own, not our config. */
const PLUGIN_HOSTS = ["dsh", "dcode"];

/** Hosts that declare no companion skill (no skills mechanism of their own). */
const NO_SKILL_HOSTS = ["devin-cli", "dcode"];

/** Hosts that declare no MCP config file of ours. */
const NO_MCP_HOSTS = ["claude-code", "qwen-code", "traecode", "dcode"];

describe("the registry", () => {
  it("holds exactly the registered hosts", () => {
    expect(HOSTS).toHaveLength(APPROVED.length);
    expect(hostNames()).toEqual(APPROVED);
  });

  it.each(APPROVED)("resolves %s by name", (name) => {
    expect(findHost(name)?.id).toBe(name);
    expect(isHostId(name)).toBe(true);
  });

  it.each(["cursor", "windsurf", "claude", ""])("rejects %s", (name) => {
    expect(findHost(name)).toBeUndefined();
    expect(isHostId(name)).toBe(false);
  });

  it("gives every non-plugin host a hook config, and every host with a mechanism the locations it needs", () => {
    for (const host of HOSTS) {
      // The plugin hosts declare no config file: their memory wiring is a
      // plugin of the host's own, so this package writes no hooks for them.
      if (!PLUGIN_HOSTS.includes(host.id)) {
        expect(host.locations.configFiles.length).toBeGreaterThan(0);
      }
      if (!NO_SKILL_HOSTS.includes(host.id)) {
        expect(host.locations.skillsDirectory.length).toBeGreaterThan(0);
      }
      // The command-line host keeps its MCP registration in the host's own
      // store, TraeCode's only correct registration is per-repository, and the
      // plugin host registers nothing: the file hosts each declare at least
      // one location.
      if (!NO_MCP_HOSTS.includes(host.id)) {
        expect(host.locations.mcpConfigFiles.length).toBeGreaterThan(0);
      }
    }
  });
});

describe("detection", () => {
  it("detects a host whose config directory already exists", async () => {
    await withFakeHome({ files: { ".claude/settings.json": "{}\n" } }, async (home) => {
      const claude = findHost("claude-code");
      expect(claude).toBeDefined();
      if (claude) {
        expect(hostIsDetected(claude, home.home)).toBe(true);
      }
      expect(detectHosts(home.home).map((host) => host.id)).toContain("claude-code");
    });
  });

  it("detects nothing on a home with no host config directory", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home) => {
      expect(detectHosts(home.home)).toEqual([]);
    });
  });
});

describe("selection", () => {
  it("explains the choice and names the all-hosts keyword when nothing was selected", async () => {
    await withFakeHome({}, async (home) => {
      const result = selectHosts([], home.home);
      expect(result.ok).toBe(false);
      if (!result.ok) {
        expect(result.message).toContain(ALL_HOSTS_KEYWORD);
        for (const name of APPROVED) {
          expect(result.message).toContain(name);
        }
      }
    });
  });

  it("quotes an unknown name and lists the accepted ones", async () => {
    await withFakeHome({}, async (home) => {
      const result = selectHosts(["cursor"], home.home);
      expect(result.ok).toBe(false);
      if (!result.ok) {
        expect(result.message).toContain('"cursor"');
        for (const name of APPROVED) {
          expect(result.message).toContain(name);
        }
      }
    });
  });

  it("wires every detected host for the all-hosts keyword", async () => {
    await withFakeHome(
      {
        files: { ".claude/settings.json": "{}\n", ".codex/hooks.json": "{}\n" },
        environment: { PATH: "" },
      },
      async (home) => {
        const result = selectHosts([ALL_HOSTS_KEYWORD], home.home);
        expect(result.ok).toBe(true);
        if (result.ok) {
          expect(result.hosts.map((host) => host.id)).toEqual(["claude-code", "codex"]);
          expect(result.detected).toEqual(["claude-code", "codex"]);
        }
      },
    );
  });

  it("asks for an explicit name when the keyword detects nothing", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home) => {
      const result = selectHosts([ALL_HOSTS_KEYWORD], home.home);
      expect(result.ok).toBe(false);
      if (!result.ok) {
        expect(result.message).toContain("explicitly");
      }
    });
  });

  it("bypasses detection for an explicit name, even on a bare home", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home) => {
      const result = selectHosts(["codebuddy"], home.home);
      expect(result.ok).toBe(true);
      if (result.ok) {
        expect(result.hosts.map((host) => host.id)).toEqual(["codebuddy"]);
      }
    });
  });

  it("selects several names once each, in registry order", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home) => {
      const result = selectHosts(["codex", "claude-code", "codex"], home.home);
      expect(result.ok).toBe(true);
      if (result.ok) {
        expect(result.hosts.map((host) => host.id)).toEqual(["claude-code", "codex"]);
      }
    });
  });
});
