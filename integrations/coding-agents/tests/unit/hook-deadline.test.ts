import { describe, expect, it } from "vitest";
import { STOP_DEADLINE_MARGIN_SECONDS, stageDeadlineMs } from "../../src/runtime/deadline";
import { budgetMilliseconds, declaredHostIds, registrationFor } from "../../src/hosts/lifecycle";

/**
 * A host enforces a budget on every hook it launches, so the memory-facing
 * stages impose their own deadline strictly inside it. The stop stage keeps a
 * fixed positive margin because it writes a whole conversation back; the
 * interactive stages take their whole budget.
 */

describe("the stop stage's self-imposed deadline", () => {
  it.each(declaredHostIds())("is strictly smaller than the host's stop budget in %s", (hostId: string) => {
    const budgetMs: number = budgetMilliseconds(registrationFor(hostId, "stop"));
    expect(stageDeadlineMs(hostId, "stop")).toBeLessThan(budgetMs);
  });

  it("is the stop budget less the fixed margin", () => {
    const budgetMs: number = budgetMilliseconds(registrationFor("claude-code", "stop"));
    expect(stageDeadlineMs("claude-code", "stop")).toBe(budgetMs - STOP_DEADLINE_MARGIN_SECONDS * 1000);
  });

  it("never goes to zero or negative", () => {
    expect(stageDeadlineMs("claude-code", "stop")).toBeGreaterThan(0);
  });
});

describe("the interactive stages' deadlines", () => {
  it.each(declaredHostIds())("take their whole budget in %s", (hostId: string) => {
    for (const stage of ["session-start", "pre-prompt"] as const) {
      expect(stageDeadlineMs(hostId, stage)).toBe(budgetMilliseconds(registrationFor(hostId, stage)));
    }
  });
});

describe("an undeclared stage", () => {
  it("is a programming error rather than a silent undefined", () => {
    expect(() => stageDeadlineMs("claude-code", "not-a-stage")).toThrow();
    expect(() => stageDeadlineMs("no-such-host", "stop")).toThrow();
  });
});