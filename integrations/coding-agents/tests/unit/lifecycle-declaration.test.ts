import { describe, expect, it } from "vitest";
import { LIFECYCLE_STAGES } from "../../src/hosts/lifecycle-types";
import {
  HOOK_SCRIPT_FILES,
  HOST_LIFECYCLES,
  STOP_DEADLINE_MARGIN_SECONDS,
  allRegistrations,
  budgetMilliseconds,
  declaredHostIds,
  hostLifecycle,
  hookContainerPath,
  hookContainerSeeds,
  hookEventPath,
  orderedRegistrations,
  registrationFor,
  stageForScriptFile,
} from "../../src/hosts/lifecycle";

/**
 * The lifecycle declaration is the one place the three lifecycle registrations
 * are described for every approved host, and both the installer and the
 * packaged entry points resolve through it. The suites here pin the parts that
 * are the compatibility surface — event spellings, script files and budgets —
 * and the sharing that makes install and runtime agree by construction.
 */

/**
 * The event spellings each host's contract fixes, per stage.
 *
 * These are the hosts' own names, not a package choice: the matcher-group hosts
 * spell them in PascalCase, and Cursor/Copilot — which read one object per
 * event — use lower camel case and their own words for the same moments.
 */
const CONTRACT_EVENTS: Record<string, Record<string, string>> = {
  "claude-code": { "session-start": "SessionStart", "pre-prompt": "UserPromptSubmit", stop: "Stop" },
  codex: { "session-start": "SessionStart", "pre-prompt": "UserPromptSubmit", stop: "Stop" },
  codebuddy: { "session-start": "SessionStart", "pre-prompt": "UserPromptSubmit", stop: "Stop" },
  workbuddy: { "session-start": "SessionStart", "pre-prompt": "UserPromptSubmit", stop: "Stop" },
  "cursor-cli": { "session-start": "sessionStart", "pre-prompt": "beforeSubmitPrompt", stop: "stop" },
  "copilot-cli": { "session-start": "sessionStart", "pre-prompt": "userPromptTransformed", stop: "agentStop" },
  // Antigravity has no SessionStart: both interactive stages declare its one
  // pre-model event, and the declaration's dedup registers it once.
  "antigravity-cli": { "session-start": "PreInvocation", "pre-prompt": "PreInvocation", stop: "Stop" },
  "devin-cli": { "session-start": "SessionStart", "pre-prompt": "UserPromptSubmit", stop: "Stop" },
  "grok-build": { "session-start": "SessionStart", "pre-prompt": "UserPromptSubmit", stop: "Stop" },
  "qwen-code": { "session-start": "SessionStart", "pre-prompt": "UserPromptSubmit", stop: "Stop" },
  "factory-droid": { "session-start": "SessionStart", "pre-prompt": "UserPromptSubmit", stop: "Stop" },
  zcode: { "session-start": "SessionStart", "pre-prompt": "UserPromptSubmit", stop: "Stop" },
  traecode: { "session-start": "SessionStart", "pre-prompt": "UserPromptSubmit", stop: "Stop" },
  kimi: { "session-start": "SessionStart", "pre-prompt": "UserPromptSubmit", stop: "Stop" },
};

describe("the shared lifecycle declaration", () => {
  it("declares exactly the three stages, in one fixed order", () => {
    expect([...LIFECYCLE_STAGES]).toEqual(["session-start", "pre-prompt", "stop"]);
  });

  it("declares a lifecycle for every host that gets hook config, and not for the plugin hosts", () => {
    // DSH's memory wiring is the Cordis plugin `@studyzy/dsh-wenyuan-memory`
    // and Dcode's is its native Agent Plugin; a command-hook lifecycle beside
    // either would recall and retain twice.
    expect(declaredHostIds().sort()).toEqual([
      "antigravity-cli",
      "claude-code",
      "codebuddy",
      "codex",
      "copilot-cli",
      "cursor-cli",
      "devin-cli",
      "factory-droid",
      "grok-build",
      "kimi",
      "qwen-code",
      "traecode",
      "workbuddy",
      "zcode",
    ]);
  });

  it("names the packaged script file for every stage, once", () => {
    expect(HOOK_SCRIPT_FILES).toEqual({
      "session-start": "session-start.mjs",
      "pre-prompt": "pre-prompt.mjs",
      stop: "stop.mjs",
    });
  });

  it.each(declaredHostIds())("gives %s the contract event spellings", (hostId: string) => {
    const spelling: Record<string, string> | undefined = CONTRACT_EVENTS[hostId];
    // Every declared host must have its spellings pinned here, so adding a host
    // without checking its contract against its own documentation fails.
    expect(spelling, `no contract spellings are pinned for ${hostId}`).toBeDefined();
    for (const stage of LIFECYCLE_STAGES) {
      expect(registrationFor(hostId, stage).event).toBe(spelling?.[stage]);
    }
  });

  it.each(declaredHostIds())("gives %s the packaged script file per stage", (hostId: string) => {
    for (const stage of LIFECYCLE_STAGES) {
      expect(registrationFor(hostId, stage).scriptFile).toBe(HOOK_SCRIPT_FILES[stage]);
    }
  });

  it.each(declaredHostIds())("bounds the interactive stages more tightly than the stop stage in %s", (hostId: string) => {
    const interactive: number = budgetMilliseconds(registrationFor(hostId, "session-start"));
    const prePrompt: number = budgetMilliseconds(registrationFor(hostId, "pre-prompt"));
    const stop: number = budgetMilliseconds(registrationFor(hostId, "stop"));
    expect(interactive).toBe(prePrompt);
    expect(stop).toBeGreaterThan(interactive);
  });

  it("keeps a positive stop deadline margin strictly inside the stop budget", () => {
    expect(STOP_DEADLINE_MARGIN_SECONDS).toBeGreaterThan(0);
    for (const hostId of declaredHostIds()) {
      const stopBudgetMs: number = budgetMilliseconds(registrationFor(hostId, "stop"));
      expect(STOP_DEADLINE_MARGIN_SECONDS * 1000).toBeLessThan(stopBudgetMs);
    }
  });

  it("returns the lifecycle for a declared host and throws for one it does not", () => {
    expect(hostLifecycle("codex")).toBe(HOST_LIFECYCLES["codex"]);
    expect(() => hostLifecycle("no-such-host")).toThrow(/no lifecycle/);
  });

  it("throws when a registration is asked for a stage the host does not declare", () => {
    expect(() => registrationFor("claude-code", "not-a-stage" as never)).toThrow();
  });

  it("orders the registrations by the shared stage order", () => {
    const ordered = orderedRegistrations(hostLifecycle("claude-code"));
    expect(ordered.map((entry) => entry.stage)).toEqual([...LIFECYCLE_STAGES]);
  });

  it("resolves a stage back from its packaged script file", () => {
    for (const stage of LIFECYCLE_STAGES) {
      expect(stageForScriptFile(HOOK_SCRIPT_FILES[stage])).toBe(stage);
    }
    expect(stageForScriptFile("not-a-script.mjs")).toBeUndefined();
  });

  it("reads a seconds budget as seconds and a milliseconds budget as milliseconds", () => {
    expect(budgetMilliseconds({ event: "E", scriptFile: "s.mjs", budget: 10, budgetUnit: "seconds", style: "matcher-group" })).toBe(10_000);
    expect(budgetMilliseconds({ event: "E", scriptFile: "s.mjs", budget: 250, budgetUnit: "milliseconds", style: "matcher-group" })).toBe(250);
  });
});

describe("the wave-4 lifecycle declarations", () => {
  it("registers Antigravity's shared PreInvocation event once, as the pre-prompt stage", () => {
    const ordered = orderedRegistrations(hostLifecycle("antigravity-cli")).map(
      ({ stage, registration }) => ({ stage, event: registration.event }),
    );
    expect(ordered).toEqual([
      { stage: "pre-prompt", event: "PreInvocation" },
      { stage: "stop", event: "Stop" },
    ]);
  });

  it("reads Qwen's and ZCode's budgets in milliseconds and everyone else's in seconds", () => {
    for (const hostId of ["qwen-code", "zcode"]) {
      expect(registrationFor(hostId, "session-start").budgetUnit).toBe("milliseconds");
      expect(registrationFor(hostId, "stop").budget).toBe(60_000);
      expect(budgetMilliseconds(registrationFor(hostId, "session-start"))).toBe(30_000);
    }
    expect(registrationFor("kimi", "stop").budgetUnit).toBe("seconds");
    expect(registrationFor("kimi", "stop").budget).toBe(60);
  });

  it("declares the process style for ZCode, the plain style for Antigravity, tables for Kimi", () => {
    expect(registrationFor("zcode", "pre-prompt").style).toBe("process-entry");
    expect(registrationFor("antigravity-cli", "stop").style).toBe("plain-entry");
    expect(registrationFor("kimi", "session-start").style).toBe("table-array");
  });

  it("carries the additional native events beside the three stages", () => {
    const grok = allRegistrations("grok-build").map(({ event }) => event);
    expect(grok).toEqual(["SessionStart", "UserPromptSubmit", "Stop", "PostToolUse"]);
    const droid = allRegistrations("factory-droid").map(({ event }) => event);
    expect(droid).toEqual(["SessionStart", "UserPromptSubmit", "Stop", "Notification"]);
    // The additional registrations reuse the shared stage's script and budget.
    const postToolUse = allRegistrations("grok-build").at(-1);
    expect(postToolUse?.scriptFile).toBe(HOOK_SCRIPT_FILES["pre-prompt"]);
    expect(postToolUse?.budget).toBe(registrationFor("grok-build", "pre-prompt").budget);
    const notification = allRegistrations("factory-droid").at(-1);
    expect(notification?.scriptFile).toBe(HOOK_SCRIPT_FILES.stop);
  });

  it("names each host's event container path", () => {
    expect(hookContainerPath("claude-code")).toEqual(["hooks"]);
    expect(hookContainerPath("factory-droid")).toEqual([]);
    expect(hookContainerPath("antigravity-cli")).toEqual(["wenyuan-memory"]);
    expect(hookContainerPath("zcode")).toEqual(["hooks", "events"]);
    expect(hookEventPath("zcode", "Stop")).toEqual(["hooks", "events", "Stop"]);
    expect(hookEventPath("factory-droid", "Stop")).toEqual(["Stop"]);
  });

  it("seeds only what the host cannot run without", () => {
    const zcode = hookContainerSeeds("zcode");
    expect(zcode).toHaveLength(2);
    const enabled = zcode.find((seed) => seed.member === "enabled");
    expect(enabled).toMatchObject({ path: ["hooks"], value: true, mode: "set" });
    const cap = zcode.find((seed) => seed.member === "maxOutputBytes");
    expect(cap).toMatchObject({ value: 32768, mode: "default" });
    const trae = hookContainerSeeds("traecode");
    expect(trae).toEqual([{ path: [], member: "version", value: 1, mode: "default" }]);
    expect(hookContainerSeeds("claude-code")).toEqual([]);
  });
});