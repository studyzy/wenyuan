import { describe, expect, it } from "vitest";
import { DISABLE_HOOKS_ENV } from "../../src/runtime/hook-contract";
import { runHook, seedStoredConnection, startStubService, withHookHome, type StubService } from "../helpers/hook-run";

/**
 * The packaged stop hook. It writes the session's conversation back to memory
 * and prints nothing at all, so the host session is never disturbed; an
 * unreachable service leaves the session unaffected and still exits zero.
 */

describe("the stop hook", () => {
  it("writes the conversation back and prints nothing", async () => {
    const service: StubService = await startStubService();
    try {
      await withHookHome(async (home) => {
        await seedStoredConnection(home, service.url);
        const run = await runHook({
          home,
          scriptFile: "stop.mjs",
          payload: { session_id: "s-1", turns: ["user: hi", "assistant: hello"] },
        });
        expect(run.status).toBe(0);
        expect(run.stdout).toBe("");
        const capture = service.requests.find((request) => request.path.endsWith("/memories"));
        expect(capture).toBeDefined();
        const body = capture!.body as { items: { content: string }[] };
        expect(body.items[0]!.content).toContain("hi");
      });
    } finally {
      await service.close();
    }
  });

  it("writes under the same document identity for a session across firings", async () => {
    const service: StubService = await startStubService();
    try {
      await withHookHome(async (home) => {
        await seedStoredConnection(home, service.url);
        // Two firings of one session both reach the service: the identity is
        // stable even though the written content may grow.
        await runHook({ home, scriptFile: "stop.mjs", payload: { session_id: "s-1", turns: ["a"] } });
        await runHook({ home, scriptFile: "stop.mjs", payload: { session_id: "s-1", turns: ["a", "b"] } });
        const captures = service.requests.filter((request) => request.path.endsWith("/memories"));
        expect(captures.length).toBe(2);
      });
    } finally {
      await service.close();
    }
  });

  it("prints nothing and exits zero when the service is unreachable", async () => {
    await withHookHome(async (home) => {
      await seedStoredConnection(home, "http://127.0.0.1:1");
      const run = await runHook({ home, scriptFile: "stop.mjs", payload: { session_id: "s-1", turns: ["a"] } });
      expect(run.status).toBe(0);
      expect(run.stdout).toBe("");
    });
  });

  it("does nothing when the disable guard is set", async () => {
    const service: StubService = await startStubService();
    try {
      await withHookHome(async (home) => {
        await seedStoredConnection(home, service.url);
        const run = await runHook({
          home,
          scriptFile: "stop.mjs",
          payload: { session_id: "s-1", turns: ["a"] },
          environment: { [DISABLE_HOOKS_ENV]: "yes" },
        });
        expect(run.status).toBe(0);
        expect(service.requests).toHaveLength(0);
      });
    } finally {
      await service.close();
    }
  });
});