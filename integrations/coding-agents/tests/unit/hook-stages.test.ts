import { describe, expect, it, vi } from "vitest";
import { runSessionStart } from "../../src/runtime/session-start-stage";
import { runPrePrompt } from "../../src/runtime/pre-prompt-stage";
import { runStop } from "../../src/runtime/stop-stage";
import { MemoryClient } from "../../src/runtime/memory-client";
import type { HookConnection } from "../../src/runtime/hook-connection";

/**
 * The three stage behaviors, exercised through an injected client so the suite
 * needs no service and no database. Each stage must never throw, must emit
 * nothing empty, and must let the host session continue when the service is
 * unreachable.
 */

const CONNECTION: HookConnection = { apiUrl: "http://127.0.0.1:18888", bankId: "b-1" };

function recallClient(texts: string[]): MemoryClient {
  const fetchImpl = vi.fn(async () =>
    new Response(JSON.stringify({ results: texts.map((text) => ({ text, id: text, type: "fact" })) }), {
      status: 200,
      headers: { "content-type": "application/json" },
    }),
  );
  return new MemoryClient({ connection: CONNECTION, timeoutMs: 1000, fetchImpl: fetchImpl as unknown as typeof fetch });
}

function failingClient(): MemoryClient {
  const fetchImpl = vi.fn(async () => {
    throw new Error("connection refused");
  });
  return new MemoryClient({ connection: CONNECTION, timeoutMs: 1000, fetchImpl: fetchImpl as unknown as typeof fetch });
}

function captureClient(onBody: (body: unknown) => void): MemoryClient {
  const fetchImpl = vi.fn(async (_url: string, init: RequestInit) => {
    onBody(JSON.parse(String(init.body)));
    return new Response(JSON.stringify({ id: "m-1" }), { status: 200, headers: { "content-type": "application/json" } });
  });
  return new MemoryClient({ connection: CONNECTION, timeoutMs: 1000, fetchImpl: fetchImpl as unknown as typeof fetch });
}

describe("the session-start stage", () => {
  it("offers a context block and a message when the service returns memories", async () => {
    const result = await runSessionStart({ eventName: "SessionStart", query: "q", timeoutMs: 1000, client: recallClient(["a", "b"]) });
    expect(result.output).not.toBeNull();
    expect(result.output?.hookSpecificOutput?.additionalContext).toBe("- a\n- b");
    expect(typeof result.output?.systemMessage).toBe("string");
  });

  it("emits nothing when the service returns no relevant memory", async () => {
    const result = await runSessionStart({ eventName: "SessionStart", query: "q", timeoutMs: 1000, client: recallClient([]) });
    expect(result.output).toBeNull();
  });

  it("emits nothing and does not throw when the service is unreachable", async () => {
    const result = await runSessionStart({ eventName: "SessionStart", query: "q", timeoutMs: 1000, client: failingClient() });
    expect(result.output).toBeNull();
  });
});

describe("the before-prompt stage", () => {
  it("returns immediately, before reading anything, when the payload carries no prompt", async () => {
    const client = recallClient(["a"]);
    const spy = vi.spyOn(client, "recall");
    const result = await runPrePrompt({
      eventName: "UserPromptSubmit",
      sessionKey: "k",
      documentId: "d",
      payload: {},
      timeoutMs: 1000,
      client,
    });
    expect(result.output).toBeNull();
    expect(result.injected).toBe(false);
    expect(spy).not.toHaveBeenCalled();
  });

  it("injects the retrieved context for the prompt", async () => {
    const result = await runPrePrompt({
      eventName: "UserPromptSubmit",
      sessionKey: `k-${Math.random()}`,
      documentId: "d",
      payload: { prompt: "what changed" },
      timeoutMs: 1000,
      client: recallClient(["a"]),
    });
    expect(result.injected).toBe(true);
    expect(result.output?.hookSpecificOutput?.additionalContext).toBe("- a");
  });

  it("consumes the empty-result budget and emits nothing rather than an empty block", async () => {
    const result = await runPrePrompt({
      eventName: "UserPromptSubmit",
      sessionKey: `k-${Math.random()}`,
      documentId: "d",
      payload: { prompt: "q" },
      timeoutMs: 1000,
      client: recallClient([]),
    });
    expect(result.output).toBeNull();
    expect(result.injected).toBe(false);
  });

  it("never throws when the service is unreachable", async () => {
    await expect(
      runPrePrompt({
        eventName: "UserPromptSubmit",
        sessionKey: `k-${Math.random()}`,
        documentId: "d",
        payload: { prompt: "q" },
        timeoutMs: 1000,
        client: failingClient(),
      }),
    ).resolves.toMatchObject({ output: null, injected: false });
  });
});

describe("the stop stage", () => {
  it("writes the conversation under a stable per-session document identity", async () => {
    const seen: unknown[] = [];
    const first = await runStop({
      hostId: "claude-code",
      payload: { session_id: "s-1", turns: ["a", "b"] },
      timeoutMs: 1000,
      client: captureClient((body) => seen.push(body)),
    });
    const second = await runStop({
      hostId: "claude-code",
      payload: { session_id: "s-1", turns: ["a", "b", "c"] },
      timeoutMs: 1000,
      client: captureClient((body) => seen.push(body)),
    });
    expect(first.written).toBe(true);
    expect(first.documentId).toBe(second.documentId);
    expect(seen[0]).toEqual({ items: [{ content: "a\nb" }] });
  });

  it("appends only the new turns when the conversation grew, given the prior turns", async () => {
    const seen: unknown[] = [];
    await runStop({
      hostId: "claude-code",
      payload: { session_id: "s-1", turns: ["a", "b"] },
      timeoutMs: 1000,
      previousTurns: ["a"],
      client: captureClient((body) => seen.push(body)),
    });
    expect(seen[0]).toEqual({ items: [{ content: "b" }] });
  });

  it("reports not-written and does not throw when the service is unreachable", async () => {
    const result = await runStop({
      hostId: "claude-code",
      payload: { session_id: "s-1", turns: ["a"] },
      timeoutMs: 1000,
      client: failingClient(),
    });
    expect(result.written).toBe(false);
  });

  it("does nothing when the payload carries no conversation", async () => {
    const client = captureClient(() => {});
    const spy = vi.spyOn(client, "capture");
    const result = await runStop({ hostId: "claude-code", payload: {}, timeoutMs: 1000, client });
    expect(result.written).toBe(false);
    expect(spy).not.toHaveBeenCalled();
  });
});