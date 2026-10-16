import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  DEFAULT_MAX_ATTEMPTS,
  WenYuanError,
  createClient,
  recall,
  reflect,
  retryAfterMs,
  retryOnCapacity,
} from "../src/index";

/** A minimal stand-in for a fetch `Response`, carrying only status and headers. */
function res(status: number, retryAfter?: string): { response: Response } {
  const headers = new Headers();
  if (retryAfter !== undefined) headers.set("retry-after", retryAfter);
  return { response: { status, headers } as unknown as Response };
}

describe("retryAfterMs", () => {
  it("reads delta-seconds", () => {
    expect(retryAfterMs(res(503, "7").response)).toBe(7000);
  });

  it("returns null when the header is absent", () => {
    expect(retryAfterMs(res(503).response)).toBeNull();
    expect(retryAfterMs(undefined)).toBeNull();
  });

  it("falls back to the caller's backoff on the HTTP-date form", () => {
    expect(retryAfterMs(res(503, "Wed, 21 Oct 2026 07:28:00 GMT").response)).toBeNull();
  });

  it("clamps a negative value to zero", () => {
    expect(retryAfterMs(res(503, "-5").response)).toBe(0);
  });
});

describe("retryOnCapacity", () => {
  it("does not retry a success", async () => {
    let calls = 0;
    const out = await retryOnCapacity(async () => {
      calls++;
      return res(200);
    }, 3, () => 0);
    expect(calls).toBe(1);
    expect(out.response?.status).toBe(200);
  });

  it("retries a 503 and returns the eventual success", async () => {
    let calls = 0;
    const out = await retryOnCapacity(async () => {
      calls++;
      return calls < 3 ? res(503, "0") : res(200);
    }, 3, () => 0);
    expect(calls).toBe(3);
    expect(out.response?.status).toBe(200);
  });

  it("retries a 429 as well", async () => {
    let calls = 0;
    await retryOnCapacity(async () => {
      calls++;
      return calls < 2 ? res(429, "0") : res(200);
    }, 3, () => 0);
    expect(calls).toBe(2);
  });

  it("gives up after maxAttempts and returns the last response", async () => {
    let calls = 0;
    const out = await retryOnCapacity(async () => {
      calls++;
      return res(503, "0");
    }, 3, () => 0);
    expect(calls).toBe(3);
    expect(out.response?.status).toBe(503);
  });

  it("does not retry other failures", async () => {
    let calls = 0;
    await retryOnCapacity(async () => {
      calls++;
      return res(400);
    }, 3, () => 0);
    expect(calls).toBe(1);
  });

  it("maxAttempts of 1 disables retrying", async () => {
    let calls = 0;
    await retryOnCapacity(async () => {
      calls++;
      return res(503, "0");
    }, 1, () => 0);
    expect(calls).toBe(1);
  });

  it("jitters the wait within Retry-After", async () => {
    // Two clients handed the same Retry-After must not wake together.
    const waits: number[] = [];
    const realSetTimeout = globalThis.setTimeout;
    globalThis.setTimeout = ((fn: () => void, ms?: number) => {
      waits.push(ms ?? 0);
      return realSetTimeout(fn, 0);
    }) as typeof setTimeout;
    try {
      for (const r of [0.1, 0.5, 0.9]) {
        await retryOnCapacity(async () => res(503, "4"), 2, () => r);
      }
    } finally {
      globalThis.setTimeout = realSetTimeout;
    }
    expect(waits.every((w) => w >= 0 && w <= 4000)).toBe(true);
    expect(new Set(waits).size).toBeGreaterThan(1);
  });
});

describe("retry cancellation", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it("does not send an already-cancelled call", async () => {
    const controller = new AbortController();
    controller.abort();
    const send = vi.fn(async () => res(200));
    await expect(retryOnCapacity(send, 3, () => 1, controller.signal)).rejects.toBe(
      controller.signal.reason,
    );
    expect(send).not.toHaveBeenCalled();
  });

  it.each([429, 503])("interrupts a %i backoff and clears its timer", async (status) => {
    const controller = new AbortController();
    const send = vi.fn(async () => res(status, "30"));
    const pending = retryOnCapacity(send, 3, () => 1, controller.signal);
    // Attach the handler before aborting: the rejection lands during abort(), and
    // an unhandled one fails the run.
    const settled = pending.catch((error: unknown) => error);
    await vi.advanceTimersByTimeAsync(0);
    expect(vi.getTimerCount()).toBe(1);
    controller.abort();
    expect(vi.getTimerCount()).toBe(0);
    expect(await settled).toBe(controller.signal.reason);
    await vi.advanceTimersByTimeAsync(60000);
    expect(send).toHaveBeenCalledTimes(1);
  });

  it("does not retry when cancelled as the response arrives", async () => {
    const controller = new AbortController();
    const send = vi.fn(async () => {
      controller.abort();
      return res(503, "30");
    });
    await expect(retryOnCapacity(send, 3, () => 1, controller.signal)).rejects.toBe(
      controller.signal.reason,
    );
    expect(send).toHaveBeenCalledTimes(1);
    expect(vi.getTimerCount()).toBe(0);
  });

  it("removes the abort listener after a successful retry", async () => {
    const controller = new AbortController();
    const remove = vi.spyOn(controller.signal, "removeEventListener");
    const send = vi.fn().mockResolvedValueOnce(res(503, "1")).mockResolvedValue(res(200));
    const pending = retryOnCapacity(send, 3, () => 1, controller.signal);
    await vi.advanceTimersByTimeAsync(1000);
    expect((await pending).response?.status).toBe(200);
    expect(remove).toHaveBeenCalledWith("abort", expect.any(Function));
    expect(vi.getTimerCount()).toBe(0);
  });

  it.each(["recall", "reflect"] as const)("threads the %s signal into backoff", async (operation) => {
    const controller = new AbortController();
    vi.spyOn(Math, "random").mockReturnValue(1);
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ detail: "busy" }), {
        status: 503,
        headers: { "Content-Type": "application/json", "Retry-After": "30" },
      }),
    );
    const client = createClient({ baseUrl: "http://localhost:18888" });
    const pending =
      operation === "recall"
        ? recall(client, "test-bank", "question", { signal: controller.signal })
        : reflect(client, "test-bank", "question", { signal: controller.signal });
    const settled = pending.catch((error: unknown) => error);
    await vi.advanceTimersByTimeAsync(0);
    expect(vi.getTimerCount()).toBe(1);
    controller.abort();
    await vi.advanceTimersByTimeAsync(0);
    expect(vi.getTimerCount()).toBe(0);
    expect(await settled).toBe(controller.signal.reason);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});

describe("client retry budget", () => {
  it("defaults to three attempts and honours an override", async () => {
    expect(DEFAULT_MAX_ATTEMPTS).toBe(3);

    const attemptCounts: Record<string, number> = {};
    for (const [label, options] of [
      ["default", {}],
      ["single", { maxAttempts: 1 }],
    ] as const) {
      let calls = 0;
      const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async () => {
        calls++;
        return new Response(JSON.stringify({ detail: "busy" }), {
          status: 503,
          headers: { "Content-Type": "application/json", "Retry-After": "0" },
        });
      });
      const client = createClient({ baseUrl: "http://localhost:18888", ...options });
      await expect(recall(client, "b", "q")).rejects.toBeInstanceOf(WenYuanError);
      attemptCounts[label] = calls;
      fetchMock.mockRestore();
    }

    expect(attemptCounts.default).toBe(DEFAULT_MAX_ATTEMPTS);
    expect(attemptCounts.single).toBe(1);
  });
});