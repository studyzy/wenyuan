import { describe, expect, it } from "vitest";
import { binOnPath, pool, semverGte, sleep } from "../../../src/core/util";

/** The retain-idempotency floor the capability probe pins: operation ids first shipped in v0.8.6. */
const MIN_IDEMPOTENT_RETAIN_VERSION = "0.8.6";

describe("semverGte", () => {
  it("compares numerically, not lexically", () => {
    expect(semverGte("0.10.0", "0.9.0")).toBe(true);
    expect(semverGte("0.9.0", "0.10.0")).toBe(false);
  });

  it("decides the retain-idempotency floor correctly", () => {
    const floor = MIN_IDEMPOTENT_RETAIN_VERSION;
    expect(floor).toBe("0.8.6");
    expect(semverGte("0.8.6", floor)).toBe(true);
    expect(semverGte("0.8.5", floor)).toBe(false); // one patch short
    expect(semverGte("0.9.0", floor)).toBe(true);
    expect(semverGte("1.0.0", floor)).toBe(true);
  });

  it("treats an unknown version as too old — a capability probe must fail closed", () => {
    expect(semverGte(undefined, "0.8.6")).toBe(false);
    expect(semverGte("", "0.8.6")).toBe(false);
    expect(semverGte("dev", "0.8.6")).toBe(false);
  });

  it("compares by the numeric part of a pre-release or build-tagged version", () => {
    expect(semverGte("0.9.0rc1", "0.8.6")).toBe(true);
    expect(semverGte("0.8.5+dev", "0.8.6")).toBe(false);
  });

  it("treats a missing patch component as zero", () => {
    expect(semverGte("0.9", "0.8.6")).toBe(true);
    expect(semverGte("0.8", "0.8.6")).toBe(false);
  });
});

describe("binOnPath", () => {
  it("finds a shell by name on PATH and rejects a nonsense name", () => {
    expect(binOnPath("sh")).toBe(true);
    expect(binOnPath("definitely-not-a-binary-xyz")).toBe(false);
  });

  it("answers a direct path by executability, not PATH", () => {
    expect(binOnPath("/bin/sh")).toBe(true);
    expect(binOnPath("/definitely/not/here")).toBe(false);
  });
});

describe("pool", () => {
  it("runs every item, never exceeds the concurrency bound, and survives item errors", async () => {
    let inFlight = 0;
    let peak = 0;
    const seen: number[] = [];
    const failed: number[] = [];
    await pool(
      [0, 1, 2, 3, 4, 5, 6, 7],
      3,
      async (n) => {
        inFlight++;
        peak = Math.max(peak, inFlight);
        await sleep(1);
        inFlight--;
        if (n === 3) throw new Error("boom");
        seen.push(n);
      },
      (i) => failed.push(i)
    );
    expect(seen.sort()).toEqual([0, 1, 2, 4, 5, 6, 7]);
    expect(failed).toEqual([3]);
    expect(peak).toBeLessThanOrEqual(3);
  });

  it("reports progress once per completed item", async () => {
    const progress: [number, number][] = [];
    await pool(["a", "b"], 2, async () => {}, undefined, (done, total) =>
      progress.push([done, total])
    );
    expect(progress).toEqual([
      [1, 2],
      [2, 2],
    ]);
  });
});
