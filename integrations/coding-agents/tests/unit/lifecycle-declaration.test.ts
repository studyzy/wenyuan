import { describe, expect, it } from "vitest";
import { LIFECYCLE_STAGES } from "../../src/hosts/lifecycle-types";
import {
  HOOK_SCRIPT_FILES,
  HOST_LIFECYCLES,
  STOP_DEADLINE_MARGIN_SECONDS,
  budgetMilliseconds,
  declaredHostIds,
  hostLifecycle,
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

/** The event spellings the contract fixes, per stage. */
const CONTRACT_EVENTS: Record<string, string> = {
  "session-start": "SessionStart",
  "pre-prompt": "UserPromptSubmit",
  stop: "Stop",
};

describe("the shared lifecycle declaration", () => {
  it("declares exactly the three stages, in one fixed order", () => {
    expect([...LIFECYCLE_STAGES]).toEqual(["session-start", "pre-prompt", "stop"]);
  });

  it("declares a lifecycle for every approved host", () => {
    expect(declaredHostIds().sort()).toEqual(["claude-code", "codebuddy", "codex"]);
  });

  it("names the packaged script file for every stage, once", () => {
    expect(HOOK_SCRIPT_FILES).toEqual({
      "session-start": "session-start.mjs",
      "pre-prompt": "pre-prompt.mjs",
      stop: "stop.mjs",
    });
  });

  it.each(declaredHostIds())("gives %s the contract event spellings", (hostId: string) => {
    for (const stage of LIFECYCLE_STAGES) {
      expect(registrationFor(hostId, stage).event).toBe(CONTRACT_EVENTS[stage]);
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