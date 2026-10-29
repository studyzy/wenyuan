import { describe, expect, it } from "vitest";
import { HOOKS_CONTAINER_KEY, MATCHER_GROUP_KEY, HOOK_COMMAND_TYPE } from "../../src/hosts/hook-shape";
import { HOST_LIFECYCLES, HOOK_SCRIPT_FILES, registrationFor } from "../../src/hosts/lifecycle";
import type { LifecycleStage } from "../../src/hosts/lifecycle-types";
import { HOSTS } from "../../src/hosts/registry";
import type { HostContribution } from "../../src/hosts/types";
import { runtimeDirectory } from "../../src/runtime/layout";
import { OWNERSHIP_MARKER } from "../../src/write/marker";
import { eventValue, eventCommandEntries } from "../helpers/hooks";
import { runInstaller, withFakeHome, type FakeHome, type JsonDocument } from "../helpers/fake-home";

/**
 * What the installer writes into a host config, read back through the shape the
 * host's loader expects. A first-batch host's event maps to a matcher group
 * holding an array with exactly one command entry; DSH wraps one flat command
 * entry in the naming member its loader reads. Either way the entry names the
 * absolute packaged script path and the host's numeric budget, and a host whose
 * loader runs a shell command spells its launcher out.
 */

/** True when a host's loader wraps the entry in a matcher group. */
function readsMatcherGroup(hostId: string): boolean {
  return registrationFor(hostId, "session-start").style === "matcher-group";
}

const CONFIG: Record<string, string> = {
  "claude-code": ".claude/settings.json",
  codex: ".codex/settings.json",
  codebuddy: ".codebuddy/settings.json",
  dsh: ".dsh/hooks/wenyuan.json",
};

const STAGES: readonly LifecycleStage[] = ["session-start", "pre-prompt", "stop"];

/**
 * The hosts that write a hook config, read from the registry so a new one is
 * covered. DSH is deliberately absent: its memory wiring is the Cordis plugin
 * `@studyzy/dsh-wenyuan-memory`, so this package writes no hooks for it.
 */
const HOST_IDS: string[] = HOSTS
  .map((host: HostContribution) => host.id)
  .filter((hostId: string) => HOST_LIFECYCLES[hostId] !== undefined);

/** The stage-to-event spelling each host declares, resolved from the declaration. */
function stageEvents(hostId: string): Map<LifecycleStage, string> {
  const map = new Map<LifecycleStage, string>();
  for (const stage of STAGES) {
    map.set(stage, registrationFor(hostId, stage).event);
  }
  return map;
}

describe("the registration shape install writes", () => {
  it.each(HOST_IDS)("writes one command entry per event in %s, wrapped as its loader reads it", async (hostId: string) => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", hostId])).status).toBe(0);
      const document: JsonDocument = await home.readJson(CONFIG[hostId]!);

      for (const event of stageEvents(hostId).values()) {
        const value = eventValue(document, event);
        if (readsMatcherGroup(hostId)) {
          expect(value, `${hostId} ${event}`).toHaveProperty(MATCHER_GROUP_KEY);
        } else {
          expect(value, `${hostId} ${event}`).not.toHaveProperty(MATCHER_GROUP_KEY);
          expect(typeof (value as JsonDocument)["command"], `${hostId} ${event}`).toBe("string");
        }
        const entries = eventCommandEntries(document, event);
        expect(entries, `${hostId} ${event}`).toHaveLength(1);
        expect(entries[0]!.timeout, `${hostId} ${event}`).toBeGreaterThan(0);
      }
    });
  });

  it.each(HOST_IDS)("names the absolute packaged script path and execution type in %s", async (hostId: string) => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", hostId])).status).toBe(0);
      const document: JsonDocument = await home.readJson(CONFIG[hostId]!);
      const staged: string = runtimeDirectory();

      const byStage = stageEvents(hostId);
      expect(byStage.size).toBe(3);
      for (const event of byStage.values()) {
        const value = eventValue(document, event) as JsonDocument;
        const entry = readsMatcherGroup(hostId)
          ? (value[MATCHER_GROUP_KEY] as JsonDocument[])[0]!
          : value;
        expect(entry["type"]).toBe(HOOK_COMMAND_TYPE);
        const command: string = String(entry["command"]);
        expect(command.startsWith(staged), `${hostId} ${event}: ${command}`).toBe(true);
        expect(command).toContain(OWNERSHIP_MARKER);
      }
    });
  });

  it("points each event at its own packaged script file", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", "claude-code"])).status).toBe(0);
      const document: JsonDocument = await home.readJson(CONFIG["claude-code"]!);
      for (const [stage, event] of stageEvents("claude-code")) {
        const entries = eventCommandEntries(document, event);
        expect(entries[0]!.command.endsWith(HOOK_SCRIPT_FILES[stage])).toBe(true);
      }
    });
  });

  it("puts nothing but the three declared events under the wrapper key", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (home: FakeHome) => {
      expect((await runInstaller(["install", "claude-code"])).status).toBe(0);
      const hooks: JsonDocument = (await home.readJson(CONFIG["claude-code"]!))[HOOKS_CONTAINER_KEY] as JsonDocument;
      expect(Object.keys(hooks).sort()).toEqual([...stageEvents("claude-code").values()].sort());
    });
  });
});