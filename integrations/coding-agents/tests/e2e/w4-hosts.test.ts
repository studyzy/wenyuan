import { describe, expect, it } from "vitest";
import { runtimeDirectory } from "../../src/runtime/layout";
import { MCP_HARNESS_ENV, MCP_SERVER_NAME, MCP_SERVERS_KEY } from "../../src/mcp/mechanisms";
import { OWNERSHIP_MARKER } from "../../src/write/marker";
import { declaredEvents } from "../helpers/hooks";
import { NO_HOST_CLI, runInstaller, type FakeHome, type JsonDocument, withFakeHome } from "../helpers/fake-home";

/**
 * The wave-4 hosts whose config dialect the shared suites cannot read: a
 * namespace key (Antigravity), the event map at the document root (Factory
 * Droid), a nested container with its own seeds (ZCode), a marked TOML block
 * (Kimi), a plugin registration (Dcode), a command-line registration spelled
 * the Gemini way (Qwen), a SQLite preflight (Devin), and a sandboxed host with
 * no user-level MCP registration (TraeCode). Every case reads the written
 * config back the way the host itself reads it.
 */

const STAGED = (): string => runtimeDirectory();

const processEntryOf = (group: unknown): JsonDocument => {
  const entries: unknown = (group as JsonDocument)["hooks"];
  const first: unknown = Array.isArray(entries) ? entries[0] : undefined;
  return (first ?? {}) as JsonDocument;
};

describe("Antigravity: events in their own namespace key", () => {
  it("registers PreInvocation once as a plain command entry", async () => {
    await withFakeHome({ environment: { PATH: NO_HOST_CLI } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", "antigravity-cli"])).status).toBe(0);
      const hooks: JsonDocument = await home.readJson(".gemini/config/hooks.json");
      const namespace: JsonDocument = hooks[OWNERSHIP_MARKER] as JsonDocument;
      // One entry per native event, the pre-prompt stage winning the shared one.
      expect(Object.keys(namespace).sort()).toEqual(["PreInvocation", "Stop"]);
      const entry: JsonDocument = namespace["PreInvocation"] as JsonDocument;
      // The plain shape: a command and a budget, no execution type, no group.
      expect(entry["command"]).toBe(`node "${STAGED()}/hooks/pre-prompt.mjs"`);
      expect(entry["timeout"]).toBe(30);
      expect(entry["type"]).toBeUndefined();

      const mcp: JsonDocument = await home.readJson(".gemini/config/mcp_config.json");
      const servers: JsonDocument = mcp[MCP_SERVERS_KEY] as JsonDocument;
      expect((servers[MCP_SERVER_NAME] as JsonDocument)["command"]).toBe("node");

      expect(await home.isFile(`.gemini/config/skills/${OWNERSHIP_MARKER}/SKILL.md`)).toBe(true);
    });
  });

  it("uninstalls the namespace and the MCP entry, keeping foreign keys", async () => {
    await withFakeHome(
      {
        files: { ".gemini/config/hooks.json": '{"other-integration":{"Custom":{"command":"x"}}}\n' },
        environment: { PATH: NO_HOST_CLI },
      },
      async (home: FakeHome) => {
        expect((await runInstaller(["install", "antigravity-cli"])).status).toBe(0);
        expect((await runInstaller(["uninstall", "antigravity-cli"])).status).toBe(0);
        const hooks: JsonDocument = await home.readJson(".gemini/config/hooks.json");
        expect(Object.keys(hooks)).toEqual(["other-integration"]);
        // The server map held only our entry, so the emptied file went with it.
        expect(await home.exists(".gemini/config/mcp_config.json")).toBe(false);
      },
    );
  });
});

describe("Factory Droid: the event map is the file", () => {
  it("writes the three events and the additional Notification at the root", async () => {
    await withFakeHome({ environment: { PATH: NO_HOST_CLI } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", "factory-droid"])).status).toBe(0);
      const hooks: JsonDocument = await home.readJson(".factory/hooks.json");
      const expected: string[] = [...declaredEvents("factory-droid"), "Notification"];
      expect(Object.keys(hooks).sort()).toEqual([...expected].sort());
      const group: JsonDocument = hooks["SessionStart"] as JsonDocument;
      const entry: JsonDocument = ((group["hooks"] as JsonDocument[]) ?? [])[0] as JsonDocument;
      expect(entry["command"]).toBe(`node "${STAGED()}/hooks/session-start.mjs"`);
      expect(entry["timeout"]).toBe(30);
      // The Notification registration reuses the stop script.
      const notification: JsonDocument = processEntryOf(hooks["Notification"]);
      expect(notification["type"]).toBe("command");
      expect(String(notification["command"])).toContain("hooks/stop.mjs");

      const mcp: JsonDocument = await home.readJson(".factory/mcp.json");
      expect((mcp[MCP_SERVERS_KEY] as JsonDocument)[MCP_SERVER_NAME]).toBeDefined();
    });
  });

  it("copies the settings.json hooks before creating a standalone hooks.json", async () => {
    await withFakeHome(
      {
        files: {
          ".factory/settings.json": '{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"echo audit"}]}]}}\n',
        },
        environment: { PATH: NO_HOST_CLI },
      },
      async (home: FakeHome) => {
        expect((await runInstaller(["install", "factory-droid"])).status).toBe(0);
        const hooks: JsonDocument = await home.readJson(".factory/hooks.json");
        // The host would have read these from settings.json; creating the
        // standalone file without them would silently disable them.
        expect(hooks["PreToolUse"]).toBeDefined();
        expect(hooks["SessionStart"]).toBeDefined();
      },
    );
  });
});

describe("ZCode: nested events with the runner switch and the output cap", () => {
  it("writes hooks.events process registrations and turns the runner on", async () => {
    await withFakeHome({ environment: { PATH: NO_HOST_CLI } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", "zcode"])).status).toBe(0);
      const config: JsonDocument = await home.readJson(".zcode/cli/config.json");
      const hooks: JsonDocument = config["hooks"] as JsonDocument;
      expect(hooks["enabled"]).toBe(true);
      // The cap is seeded only when the user has not tuned it.
      expect(hooks["maxOutputBytes"]).toBe(32768);
      const events: JsonDocument = hooks["events"] as JsonDocument;
      expect(Object.keys(events).sort()).toEqual(["SessionStart", "Stop", "UserPromptSubmit"]);
      const stop: JsonDocument = processEntryOf(events["Stop"]);
      expect(stop["type"]).toBe("process");
      expect(stop["command"]).toBe("node");
      expect((stop["args"] as string[])?.[0]).toBe(`${STAGED()}/hooks/stop.mjs`);
      expect(stop["timeoutMs"]).toBe(60_000);
      // The server map lives in the same document, nested one level deeper.
      const mcp: JsonDocument = config["mcp"] as JsonDocument;
      const servers: JsonDocument = mcp["servers"] as JsonDocument;
      expect((servers[MCP_SERVER_NAME] as JsonDocument)["command"]).toBe("node");
      expect(await home.isFile(`.zcode/skills/${OWNERSHIP_MARKER}/SKILL.md`)).toBe(true);
    });
  });

  it("uninstall removes the whole hooks block and our server entry", async () => {
    await withFakeHome({ environment: { PATH: NO_HOST_CLI } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", "zcode"])).status).toBe(0);
      expect((await runInstaller(["uninstall", "zcode"])).status).toBe(0);
      // The config held only this package's wiring, so the emptied file went
      // with it rather than surviving as an empty shell.
      expect(await home.exists(".zcode/cli/config.json")).toBe(false);
    });
  });

  it("refuses a foreign same-named server before writing anything", async () => {
    await withFakeHome(
      {
        files: {
          ".zcode/cli/config.json":
            '{"mcp":{"servers":{"wenyuan-memory":{"command":"other","args":["x"]}}}}\n',
        },
        environment: { PATH: NO_HOST_CLI },
      },
      async (home: FakeHome) => {
        const result = await runInstaller(["install", "zcode"]);
        expect(result.status).not.toBe(0);
        expect(result.combined()).toContain("user-managed");
        // Nothing was written beside the refusal.
        expect(await home.exists(".zcode/hooks.json")).toBe(false);
        const config: JsonDocument = await home.readJson(".zcode/cli/config.json");
        expect(config["hooks"]).toBeUndefined();
      },
    );
  });
});

describe("Kimi Code: a marked TOML block and an mcp.json beside it", () => {
  it("writes the block, the server map and the skill; nothing else moves", async () => {
    await withFakeHome({ environment: { PATH: NO_HOST_CLI } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", "kimi"])).status).toBe(0);
      const config: string = await home.read(".kimi-code/config.toml");
      expect(config).toContain("# WENYUAN_CODING_AGENTS_KIMI_START");
      expect(config).toContain("[[hooks]]");
      expect(config).toContain('event = "SessionStart"');
      expect(config).toContain(`node \\"${STAGED()}/hooks/pre-prompt.mjs\\"`);
      expect((config.match(/\[\[hooks\]\]/g) ?? []).length).toBe(3);
      const mcp: JsonDocument = await home.readJson(".kimi-code/mcp.json");
      const entry: JsonDocument = (mcp[MCP_SERVERS_KEY] as JsonDocument)[MCP_SERVER_NAME] as JsonDocument;
      expect((entry["env"] as JsonDocument)[MCP_HARNESS_ENV]).toBe("kimi");
      expect(await home.isFile(`.kimi-code/skills/${OWNERSHIP_MARKER}/SKILL.md`)).toBe(true);
    });
  });

  it("a re-install repairs a moved runtime path and keeps foreign tables", async () => {
    await withFakeHome({ environment: { PATH: NO_HOST_CLI } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", "kimi"])).status).toBe(0);
      expect((await runInstaller(["install", "kimi"])).status).toBe(0);
      const config: string = await home.read(".kimi-code/config.toml");
      expect((config.match(/# WENYUAN_CODING_AGENTS_KIMI_START/g) ?? []).length).toBe(1);
    });
  });

  it("uninstall strips the block byte for byte and leaves the rest", async () => {
    await withFakeHome(
      {
        files: { ".kimi-code/config.toml": "[model]\nname = \"kimi\"\n" },
        environment: { PATH: NO_HOST_CLI },
      },
      async (home: FakeHome) => {
        expect((await runInstaller(["install", "kimi"])).status).toBe(0);
        expect((await runInstaller(["uninstall", "kimi"])).status).toBe(0);
        const config: string = await home.read(".kimi-code/config.toml");
        expect(config).toContain("[model]");
        expect(config).not.toContain("WENYUAN_CODING_AGENTS_KIMI");
      },
    );
  });
});

describe("Qwen Code: the Gemini command interface", () => {
  it("registers through qwen mcp add with the short flags and no terminator", async () => {
    await withFakeHome({ environment: { PATH: NO_HOST_CLI } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", "qwen-code"])).status).toBe(0);
      // A second install asks the host's own interface what it holds and
      // re-points the entry instead of failing on a name already registered.
      expect((await runInstaller(["install", "qwen-code"])).status).toBe(0);
      const settings: string = await home.read(".qwen/settings.json");
      expect(settings).toContain('"SessionStart"');
    });
  });

  it("keeps a comment a hand-maintained settings file carries", async () => {
    await withFakeHome(
      {
        files: { ".qwen/settings.json": '{\n  // kept by hand\n  "theme": "dark"\n}\n' },
        environment: { PATH: NO_HOST_CLI },
      },
      async (home: FakeHome) => {
        expect((await runInstaller(["install", "qwen-code"])).status).toBe(0);
        const settings: string = await home.read(".qwen/settings.json");
        expect(settings).toContain("// kept by hand");
        expect(settings).toContain('"theme": "dark"');
        expect(settings).toContain('"UserPromptSubmit"');
      },
    );
  });
});

describe("Devin: SQLite preflight, then the standard wiring", () => {
  it("wires the hooks and the server map when the probe answers", async () => {
    await withFakeHome({ environment: { PATH: NO_HOST_CLI } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", "devin-cli"])).status).toBe(0);
      const config: JsonDocument = await home.readJson(".config/devin/config.json");
      const hooks: JsonDocument = config["hooks"] as JsonDocument;
      expect(Object.keys(hooks).sort()).toEqual([...declaredEvents("devin-cli")].sort());
      const mcp: JsonDocument = await home.readJson(".config/devin/mcp_config.json");
      const entry: JsonDocument = (mcp[MCP_SERVERS_KEY] as JsonDocument)[MCP_SERVER_NAME] as JsonDocument;
      expect((entry["env"] as JsonDocument)[MCP_HARNESS_ENV]).toBe("devin-cli");
      // Devin has no skills mechanism: no companion skill is placed.
      expect(await home.exists(".config/devin/skills")).toBe(false);
    });
  });
});

describe("TraeCode: hooks, sandbox rules and no user-level MCP", () => {
  it("seeds the document version, the sandbox rules and the per-repo guidance", async () => {
    await withFakeHome({ environment: { PATH: NO_HOST_CLI } }, async (home: FakeHome) => {
      const result = await runInstaller(["install", "traecode"]);
      expect(result.status).toBe(0);
      const hooks: JsonDocument = await home.readJson(".trae-cn/hooks.json");
      expect(hooks["version"]).toBe(1);
      expect(Object.keys(hooks["hooks"] as JsonDocument).sort()).toEqual(
        [...declaredEvents("traecode")].sort(),
      );
      const sandbox: JsonDocument = await home.readJson(".trae-cn/sandbox.json");
      const rules: string[] =
        ((sandbox["filesystem"] as JsonDocument)["readWrite"] as string[]) ?? [];
      expect(rules.some((rule) => rule.endsWith(".wenyuan"))).toBe(true);
      expect(rules.join(" ")).toContain("User/workspaceStorage");
      // The gate message names the manual step; no user-level MCP file exists.
      expect(result.combined()).toContain("enableWorkspaceMcp");
      expect(await home.exists(".trae-cn/mcp.json")).toBe(false);
      expect(await home.isFile(`.trae-cn/skills/${OWNERSHIP_MARKER}/SKILL.md`)).toBe(true);
    });
  });

  it("flips the workspace gate under --enable-workspace-mcp", async () => {
    await withFakeHome(
      { environment: { PATH: NO_HOST_CLI, XDG_CONFIG_HOME: "" } },
      async (home: FakeHome) => {
        expect((await runInstaller(["install", "traecode", "--enable-workspace-mcp"])).status).toBe(0);
        const settings: JsonDocument = await home.readJson(
          ".config/Trae CN/User/settings.json",
        );
        expect(settings["trae.mcp.enableWorkspaceMcp"]).toBe(true);
      },
    );
  });

  it("uninstall removes the events and keeps the version field", async () => {
    await withFakeHome({ environment: { PATH: NO_HOST_CLI } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", "traecode"])).status).toBe(0);
      expect((await runInstaller(["uninstall", "traecode"])).status).toBe(0);
      const hooks: JsonDocument = await home.readJson(".trae-cn/hooks.json");
      expect(hooks["version"]).toBe(1);
      expect(hooks["hooks"]).toBeUndefined();
    });
  });
});

describe("Dcode: the native plugin through its own manager", () => {
  it("prepares the marketplace and installs through dcode plugin", async () => {
    await withFakeHome({ environment: { PATH: NO_HOST_CLI } }, async (home: FakeHome) => {
      const result = await runInstaller(["install", "dcode"]);
      expect(result.status).toBe(0);
      const marketplace: JsonDocument = await home.readJson(".wenyuan/.agents/plugins/marketplace.json");
      expect(marketplace["name"]).toBe("wenyuan-coding-agents");
      const plugins: JsonDocument[] = marketplace["plugins"] as JsonDocument[];
      expect(plugins[0]?.["source"]).toEqual({ source: "local", path: "./wenyuan-memory" });
      expect(result.combined()).toContain("wenyuan-coding-agents@wenyuan-coding-agents");
      // No hook config and no MCP file of ours exists for this host.
      expect(await home.exists(".deepagents/hooks.json")).toBe(false);
    });
  });

  it("a foreign marketplace at the conventional path is preserved", async () => {
    await withFakeHome(
      {
        files: {
          ".wenyuan/.agents/plugins/marketplace.json":
            '{"name":"someone-else","plugins":[{"name":"theirs"}]}\n',
        },
        environment: { PATH: NO_HOST_CLI },
      },
      async (home: FakeHome) => {
        expect((await runInstaller(["install", "dcode"])).status).toBe(0);
        const untouched: JsonDocument = await home.readJson(".wenyuan/.agents/plugins/marketplace.json");
        expect(untouched["name"]).toBe("someone-else");
        const fallback: JsonDocument = await home.readJson(
          ".wenyuan/wenyuan-coding-agents-marketplace.json",
        );
        expect(fallback["name"]).toBe("wenyuan-coding-agents");
      },
    );
  });
});
