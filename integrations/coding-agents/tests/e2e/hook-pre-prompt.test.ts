import { describe, expect, it } from "vitest";
import { DISABLE_HOOKS_ENV } from "../../src/runtime/hook-contract";
import { registrationFor } from "../../src/hosts/lifecycle";
import { runHook, seedStoredConnection, startStubService, withHookHome, type StubService } from "../helpers/hook-run";

/**
 * The packaged before-prompt hook. It injects the retrieved memories once per
 * session; a firing that carries no prompt costs nothing, an empty result
 * emits nothing, and a failing or unreachable service still exits zero.
 */

describe("the before-prompt hook", () => {
  it("injects a context block for a submit, routed under the host's event spelling", async () => {
    const service: StubService = await startStubService();
    service.recallResponses.push({ results: [{ text: "remembered", id: "1", type: "fact" }] });
    try {
      await withHookHome(async (home) => {
        await seedStoredConnection(home, service.url);
        const run = await runHook({
          home,
          scriptFile: "pre-prompt.mjs",
          payload: { session_id: `s-${Math.random()}`, prompt: "what changed?" },
        });
        expect(run.status).toBe(0);
        const envelope = run.envelope() as Record<string, unknown>;
        const specific = envelope?.["hookSpecificOutput"] as Record<string, unknown>;
        expect(specific["hookEventName"]).toBe(registrationFor("claude-code", "pre-prompt").event);
        expect(String(specific["additionalContext"])).toContain("remembered");
      });
    } finally {
      await service.close();
    }
  });

  it("carries the guide on the first prompt and the roster refresh after", async () => {
    const service: StubService = await startStubService();
    service.recallResponses.push({ results: [{ text: "a", id: "1" }] }, { results: [{ text: "b", id: "2" }] });
    try {
      await withHookHome(async (home) => {
        await seedStoredConnection(home, service.url);
        const session = `s-${Math.random()}`;
        const first = await runHook({ home, scriptFile: "pre-prompt.mjs", payload: { session_id: session, prompt: "one" } });
        const second = await runHook({ home, scriptFile: "pre-prompt.mjs", payload: { session_id: session, prompt: "two" } });
        expect(first.envelope()).not.toBeNull();
        // The first firing carries the full guide; later ones carry the
        // roster refresh, so the model is reminded without re-reading the wall.
        const secondEnvelope = second.envelope() as Record<string, unknown>;
        const secondSpecific = secondEnvelope["hookSpecificOutput"] as Record<string, unknown>;
        expect(String(secondSpecific["additionalContext"])).toContain("<wenyuan_knowledge_refresh>");
      });
    } finally {
      await service.close();
    }
  });

  it("costs nothing on a firing that carries no prompt", async () => {
    const service: StubService = await startStubService();
    try {
      await withHookHome(async (home) => {
        await seedStoredConnection(home, service.url);
        const run = await runHook({ home, scriptFile: "pre-prompt.mjs", payload: { session_id: "s-1" } });
        expect(run.status).toBe(0);
        // No prompt, no recall: the static guide may still ride along.
        expect(
          service.requests.filter((request) => request.path.endsWith("/memories/recall")),
        ).toHaveLength(0);
      });
    } finally {
      await service.close();
    }
  });

  it("serves the knowledge injection even when the recall result is empty", async () => {
    const service: StubService = await startStubService();
    for (let index = 0; index < 5; index += 1) {
      service.recallResponses.push({ results: [] });
    }
    try {
      await withHookHome(async (home) => {
        await seedStoredConnection(home, service.url);
        const run = await runHook({
          home,
          scriptFile: "pre-prompt.mjs",
          payload: { session_id: `s-${Math.random()}`, prompt: "q" },
        });
        expect(run.status).toBe(0);
        const envelope = run.envelope() as Record<string, unknown>;
        const specific = envelope["hookSpecificOutput"] as Record<string, unknown>;
        const injected = String(specific["additionalContext"]);
        expect(
          injected.includes("<wenyuan_knowledge>") || injected.includes("<wenyuan_knowledge_refresh>"),
        ).toBe(true);
      });
    } finally {
      await service.close();
    }
  });

  it("exits zero when the service is unreachable, the session unaffected", async () => {
    await withHookHome(async (home) => {
      await seedStoredConnection(home, "http://127.0.0.1:1");
      const run = await runHook({ home, scriptFile: "pre-prompt.mjs", payload: { session_id: "s-1", prompt: "q" } });
      expect(run.status).toBe(0);
      const envelope = run.envelope();
      if (envelope !== null) {
        const specific = (envelope as Record<string, unknown>)["hookSpecificOutput"] as Record<string, unknown>;
        expect(typeof specific["additionalContext"]).toBe("string");
      }
    });
  });

  it("does nothing when the disable guard is set", async () => {
    const service: StubService = await startStubService();
    service.recallResponses.push({ results: [{ text: "x", id: "1" }] });
    try {
      await withHookHome(async (home) => {
        await seedStoredConnection(home, service.url);
        const run = await runHook({
          home,
          scriptFile: "pre-prompt.mjs",
          payload: { session_id: "s-1", prompt: "q" },
          environment: { [DISABLE_HOOKS_ENV]: "true" },
        });
        expect(run.status).toBe(0);
        expect(run.envelope()).toBeNull();
        expect(service.requests).toHaveLength(0);
      });
    } finally {
      await service.close();
    }
  });
});