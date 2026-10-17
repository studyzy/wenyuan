import { describe, expect, it } from "vitest";
import {
  emptySessionState,
  readSessionState,
  sessionKey,
  sessionStatePath,
  writeSessionState,
} from "../../src/runtime/hook-state";
import { HARNESS_ENV, resolveHostId } from "../../src/runtime/hook-host";
import { declaredHostIds } from "../../src/hosts/lifecycle";
import { withFakeHome, type FakeHome } from "../helpers/fake-home";

/**
 * The per-session progress the stage behaviors share, and how a packaged hook
 * resolves which host it is firing for. The state is keyed by a stable
 * per-session identity; a state that cannot be read is treated as fresh, and a
 * state that cannot be written never fails the hook.
 */

describe("the per-session state key", () => {
  it("is stable for one session and distinct across sessions and hosts", () => {
    expect(sessionKey("claude-code", "s-1")).toBe(sessionKey("claude-code", "s-1"));
    expect(sessionKey("claude-code", "s-1")).not.toBe(sessionKey("claude-code", "s-2"));
    expect(sessionKey("claude-code", "s-1")).not.toBe(sessionKey("codex", "s-1"));
  });

  it("places one small JSON file per session under the per-user directory", () => {
    expect(sessionStatePath("abc")).toMatch(/hook-state[\\/]abc\.json$/);
  });

  it("starts a session fresh", () => {
    expect(emptySessionState("d-1")).toEqual({ injected: false, attempts: 0, documentId: "d-1" });
  });
});

describe("reading and writing the state", () => {
  it("round-trips a written state", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (_home: FakeHome) => {
      const key = sessionKey("claude-code", "s-1");
      await writeSessionState(key, { injected: true, attempts: 2, documentId: "d-1" });
      await expect(readSessionState(key, "fallback")).resolves.toEqual({ injected: true, attempts: 2, documentId: "d-1" });
    });
  });

  it("treats a missing or unreadable file as fresh rather than failing", async () => {
    await withFakeHome({ environment: { PATH: "" } }, async (_home: FakeHome) => {
      await expect(readSessionState("never-written", "d-1")).resolves.toEqual({
        injected: false,
        attempts: 0,
        documentId: "d-1",
      });
    });
  });
});

describe("resolving the firing host", () => {
  it("prefers a declared host named by the payload", () => {
    expect(resolveHostId({ host: "codex" }, {})).toBe("codex");
  });

  it("falls back to the harness variable when the payload names none", () => {
    expect(resolveHostId({}, { [HARNESS_ENV]: "codebuddy" })).toBe("codebuddy");
  });

  it("ignores a payload or harness value that names no declared host", () => {
    const fallback: string | undefined = resolveHostId({ host: "nope" }, { [HARNESS_ENV]: "also-nope" });
    expect(declaredHostIds()).toContain(fallback);
  });

  it("always yields a declared host, so a hook always has a lifecycle", () => {
    expect(declaredHostIds()).toContain(resolveHostId({}, {}));
  });
});