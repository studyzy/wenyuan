import { describe, expect, it } from "vitest";
import { DISABLE_HOOKS_ENV } from "../../src/runtime/hook-contract";
import { registrationFor } from "../../src/hosts/lifecycle";
import { runHook, seedStoredConnection, startStubService, withHookHome, type StubService } from "../helpers/hook-run";

/**
 * The packaged session-start hook, launched as a host launches it: by absolute
 * path, with the lifecycle payload on standard input. When the service is
 * reachable and returns memory it writes one envelope carrying a user message
 * and a context block; otherwise it writes nothing and still exits zero.
 */

describe("the session-start hook", () => {
  it("prints one envelope with a message and a context block when memory is returned", async () => {
    const service: StubService = await startStubService();
    service.recallResponses.push({ results: [{ text: "the user works in Go", id: "1", type: "fact" }] });
    try {
      await withHookHome(async (home) => {
        await seedStoredConnection(home, service.url);
        const run = await runHook({
          home,
          scriptFile: "session-start.mjs",
          payload: { session_id: "s-1", host: "claude-code" },
        });
        expect(run.status).toBe(0);
        const envelope = run.envelope() as Record<string, unknown>;
        expect(envelope).not.toBeNull();
        expect(typeof envelope["systemMessage"]).toBe("string");
        const specific = envelope["hookSpecificOutput"] as Record<string, unknown>;
        expect(specific["hookEventName"]).toBe(registrationFor("claude-code", "session-start").event);
        expect(String(specific["additionalContext"])).toContain("the user works in Go");
      });
    } finally {
      await service.close();
    }
  });

  it("prints nothing and exits zero when the service returns no memory", async () => {
    const service: StubService = await startStubService();
    service.recallResponses.push({ results: [] });
    try {
      await withHookHome(async (home) => {
        await seedStoredConnection(home, service.url);
        const run = await runHook({ home, scriptFile: "session-start.mjs", payload: { session_id: "s-1" } });
        expect(run.status).toBe(0);
        expect(run.envelope()).toBeNull();
      });
    } finally {
      await service.close();
    }
  });

  it("leaves the session unaffected when the service is unreachable", async () => {
    await withHookHome(async (home) => {
      // A port nothing listens on: the hook must fail open, not hang or throw.
      await seedStoredConnection(home, "http://127.0.0.1:1");
      const run = await runHook({ home, scriptFile: "session-start.mjs", payload: { session_id: "s-1" } });
      expect(run.status).toBe(0);
      expect(run.envelope()).toBeNull();
    });
  });

  it("does nothing at all when the disable guard is set", async () => {
    const service: StubService = await startStubService();
    service.recallResponses.push({ results: [{ text: "x", id: "1" }] });
    try {
      await withHookHome(async (home) => {
        await seedStoredConnection(home, service.url);
        const run = await runHook({
          home,
          scriptFile: "session-start.mjs",
          payload: { session_id: "s-1" },
          environment: { [DISABLE_HOOKS_ENV]: "1" },
        });
        expect(run.status).toBe(0);
        expect(run.envelope()).toBeNull();
        expect(service.requests).toHaveLength(0);
      });
    } finally {
      await service.close();
    }
  });

  it("never fails on an empty or malformed payload", async () => {
    await withHookHome(async (home) => {
      await seedStoredConnection(home, "http://127.0.0.1:1");
      for (const payload of ["", "not json", "[]"]) {
        const run = await runHook({ home, scriptFile: "session-start.mjs", payload });
        expect(run.status, payload).toBe(0);
      }
    });
  });
});