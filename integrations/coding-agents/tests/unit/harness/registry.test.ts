import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { getHarness, HARNESS_NAMES, PLUGIN_ENTRYPOINTS } from "../../../src/harness/registry";

describe("HARNESS_NAMES", () => {
  it("lists all registered harnesses", () => {
    expect(HARNESS_NAMES).toEqual(
      expect.arrayContaining([
        "opencode",
        "opencode2",
        "kilo",
        "cline-cli",
        "pi",
        "prime-agent",
        "dsh",
        "claude-code",
        "cursor-cli",
        "codex",
        "dcode",
        "antigravity-cli",
        "devin-cli",
        "copilot-cli",
        "grok-build",
        "qwen-code",
        "factory-droid",
        "zcode",
        "traecode",
        "kimi-code",
        "workbuddy",
        "codebuddy",
      ])
    );
    expect(HARNESS_NAMES).toHaveLength(22);
  });
});

describe("getHarness", () => {
  it("resolves hook harnesses without touching the plugin adapters", async () => {
    for (const name of [
      "claude-code",
      "cursor-cli",
      "codex",
      "dcode",
      "antigravity-cli",
      "devin-cli",
    ]) {
      const adapter = await getHarness(name);
      expect(adapter.name).toBe(name);
      // Lightweight hook adapters have no persistent runtime — createRuntime always throws before
      // touching its argument, so a stand-in value is fine here.
      expect(() => adapter.createRuntime({} as never)).toThrow();
    }
  });

  it("resolves the opencode adapter by name", async () => {
    const adapter = await getHarness("opencode");
    expect(adapter.name).toBe("opencode");
    // Deliberate invariant: opencode via the registry is a no-runtime adapter (same shape as the
    // hook harnesses) — the real opencode runtime is built by src/plugins/opencode.ts importing
    // the adapter directly, bypassing this registry. Lock it so a future change can't silently
    // make this look functional.
    expect(() => adapter.createRuntime({} as never)).toThrow();
  });

  it("resolves opencode2 as its own plugin harness, separate from v1", async () => {
    // Same no-runtime shape as opencode above, but a DIFFERENT entrypoint: v2's plugin contract
    // shares nothing with v1's, so handing either host the other's export loads a plugin that
    // registers nothing and reports no error.
    const adapter = await getHarness("opencode2");
    expect(adapter.name).toBe("opencode2");
    expect(() => adapter.createRuntime({} as never)).toThrow(/src\/plugins\/opencode2\.ts/);
  });

  it("resolves Cline as a native-plugin harness rather than a hook binary", async () => {
    const adapter = await getHarness("cline-cli");
    expect(adapter.name).toBe("cline-cli");
    expect(() => adapter.createRuntime({} as never)).toThrow(/src\/plugins\/cline\.ts/);
  });

  it("resolves pi and its Prime Agent fork as separate extension harnesses", async () => {
    for (const [name, entry] of [
      ["pi", /src\/plugins\/pi\.ts/],
      ["prime-agent", /src\/plugins\/prime-agent\.ts/],
    ] as const) {
      const adapter = await getHarness(name);
      expect(adapter.name).toBe(name);
      // Both drive the same shared adapter (harness/pi-extension.ts) but are wired by their own
      // entrypoint, so each must resolve to its own dist bundle — swapping them would silently
      // retain one host's sessions under the other's bank.
      expect(() => adapter.createRuntime({} as never)).toThrow(entry);
    }
  });

  it("resolves DeepSeek Harness as a native Cordis-plugin harness", async () => {
    const adapter = await getHarness("dsh");
    expect(adapter.name).toBe("dsh");
    expect(() => adapter.createRuntime({} as never)).toThrow(/src\/plugins\/dsh\.ts/);
  });

  it("rejects unknown harness names", async () => {
    await expect(getHarness("nope")).rejects.toThrow(/unknown harness/);
  });
});

/**
 * The registry and the installer are separate lists that must agree: the background git-diff
 * enrichment resolves a harness through the registry, so an installer the registry doesn't know
 * dies with "unknown harness" — and the reverse direction leaves a registry name the installer
 * cannot wire, advertised but unusable. Both checks stay skipped until those ports land, then
 * go live without an edit.
 */
describe("registry covers every installable harness", () => {
  const ROOT = fileURLToPath(new URL("../../..", import.meta.url));
  const INSTALLER_TS = join(ROOT, "src/installer.ts");
  const HOOK_LIFECYCLE_TS = join(ROOT, "src/harness/hook-lifecycle.ts");

  it("has no harness the installer wires but the registry rejects", async ({ skip }) => {
    if (!existsSync(INSTALLER_TS)) return skip("deferred: the installer port has not landed");
    const { INSTALLERS } = (await import(INSTALLER_TS)) as { INSTALLERS: { name: string }[] };
    expect(INSTALLERS.map((i) => i.name).filter((n) => !HARNESS_NAMES.includes(n))).toEqual([]);
  });

  it("has no hook harness the registry names but the installer cannot wire", async ({ skip }) => {
    if (!existsSync(HOOK_LIFECYCLE_TS) || !existsSync(INSTALLER_TS))
      return skip("deferred: the hook-lifecycle / installer ports have not landed");
    const { INSTALLERS } = (await import(INSTALLER_TS)) as { INSTALLERS: { name: string }[] };
    const { HOOK_HARNESSES } = (await import(HOOK_LIFECYCLE_TS)) as {
      HOOK_HARNESSES: Record<string, unknown>;
    };
    const installable = new Set(INSTALLERS.map((i) => i.name));
    expect(Object.keys(HOOK_HARNESSES).filter((n) => !installable.has(n))).toEqual([]);
  });
});

/**
 * The harness name an entrypoint reports is a bare string literal that nothing else checks: it
 * selects the `harnesses.<name>` config section, feeds `{harness}` bank templating, and is stamped
 * on every document that host retains. A typo there ships green — the registry and the entrypoints
 * are SEPARATE hand-maintained lists, so neither notices that the running plugin calls itself
 * something else.
 *
 * So assert it over the whole family, enumerated from the registry rather than a second list: every
 * entrypoint the registry names must report the harness the registry maps it to.
 */
describe("every plugin entrypoint reports the harness the registry maps it to", () => {
  const PKG = fileURLToPath(new URL("../../..", import.meta.url));

  /** The two idioms an entrypoint uses to name itself: the argument it hands its shared factory
   *  (opencode/Kilo via createPluginEntry, opencode 2 via createOpencode2PluginEntry, pi/Prime Agent
   *  via createPiExtension), or its own module constant (Cline and dsh, which build their runtime
   *  themselves). Matching the factory NAMES rather than a generic call keeps the guard honest: an
   *  entrypoint that names itself some new way fails here until this list is widened, which is
   *  exactly the review that a new host needs.  */
  const DECLARES_HARNESS =
    /(?:createPluginEntry|createOpencode2PluginEntry|createPiExtension)\("([^"]+)"\)|const HARNESS = "([^"]+)"/;

  for (const [harness, entry] of Object.entries(PLUGIN_ENTRYPOINTS)) {
    const file = join(PKG, entry as string);
    // Entrypoints another wave owns stay visible as skips, then go live when they land.
    (existsSync(file) ? it : it.skip)(`${harness} (${entry})`, () => {
      const declaration = readFileSync(file, "utf8").match(DECLARES_HARNESS);
      expect(declaration, `${entry} names no harness in a form this guard recognises`).not.toBeNull();
      expect(declaration![1] ?? declaration![2]).toBe(harness);
    });
  }
});
