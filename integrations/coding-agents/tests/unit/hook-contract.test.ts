import { Readable } from "node:stream";
import { describe, expect, it, vi } from "vitest";
import {
  DISABLE_HOOKS_ENV,
  EMPTY_RESULT_BUDGET,
  RETRY_BUDGET,
  failOpen,
  hooksDisabled,
  lifecyclePayload,
  parsePayload,
  payloadString,
  readStdin,
} from "../../src/runtime/hook-contract";

/**
 * The cross-cutting contract every packaged hook shares: a disable guard that
 * makes every path a no-op, a fail-open wrapper that never lets a failure
 * escape, the bounded budgets the memory-facing stages share, and the payload
 * reader. Nothing here may throw to the host.
 */

describe("the disable guard", () => {
  it("keeps its legacy deployment-contract spelling", () => {
    expect(DISABLE_HOOKS_ENV).toBe("WENYUAN_DISABLE_HOOKS");
  });

  it("treats an absent variable, and the usual off spellings, as enabled", () => {
    for (const value of [undefined, "", "0", "false", "no", "off", "OFF"]) {
      const environment: NodeJS.ProcessEnv = value === undefined ? {} : { [DISABLE_HOOKS_ENV]: value };
      expect(hooksDisabled(environment), String(value)).toBe(false);
    }
  });

  it("treats any other value as disabled", () => {
    for (const value of ["1", "true", "yes", "on", "anything"]) {
      expect(hooksDisabled({ [DISABLE_HOOKS_ENV]: value }), value).toBe(true);
    }
  });
});

describe("the fail-open wrapper", () => {
  it("returns the body's value on success", async () => {
    await expect(failOpen(() => 7)).resolves.toEqual({ ok: true, value: 7 });
  });

  it("returns a failure result rather than throwing when the body throws", async () => {
    const outcome = await failOpen(() => {
      throw new Error("boom");
    });
    expect(outcome).toEqual({ ok: false });
  });

  it("returns a failure result when the body rejects", async () => {
    await expect(failOpen(async () => Promise.reject(new Error("no")))).resolves.toEqual({ ok: false });
  });

  it("reports the failure without letting a failing handler escape", async () => {
    const onFailure = vi.fn(() => {
      throw new Error("handler also failed");
    });
    await expect(failOpen(() => Promise.reject(new Error("x")), onFailure)).resolves.toEqual({ ok: false });
    expect(onFailure).toHaveBeenCalledOnce();
  });
});

describe("the shared budgets", () => {
  it("bounds the retries and the empty-result budget", () => {
    expect(RETRY_BUDGET).toBeGreaterThan(0);
    expect(EMPTY_RESULT_BUDGET).toBeGreaterThan(0);
  });
});

describe("reading the host payload", () => {
  it("reads all of a stream as text", async () => {
    await expect(readStdin(Readable.from([Buffer.from("{\"a\":1}")]))).resolves.toBe("{\"a\":1}");
  });

  it("returns an empty string when the stream errors", async () => {
    const stream = new Readable({
      read() {
        this.destroy(new Error("broken pipe"));
      },
    });
    await expect(readStdin(stream)).resolves.toBe("");
  });

  it("parses a JSON object, and any other input becomes an empty object", () => {
    expect(parsePayload("{\"prompt\":\"hi\"}")).toEqual({ prompt: "hi" });
    for (const source of ["", "   ", "not json", "[1,2]", "null", "\"str\""]) {
      expect(parsePayload(source), source).toEqual({});
    }
  });

  it("adds the lifecycle members a host exports in the environment", () => {
    // DSH launches a hook with no payload on standard input and the firing
    // event in the environment instead.
    const environment: NodeJS.ProcessEnv = {
      WENYUAN_HOOK_EVENT: "SessionStart",
      WENYUAN_HOOK_HOST: "dsh",
    };
    expect(lifecyclePayload("", environment)).toEqual({ hook_event_name: "SessionStart", host: "dsh" });
    expect(lifecyclePayload("{\"session_id\":\"s-1\"}", environment)).toEqual({
      session_id: "s-1",
      hook_event_name: "SessionStart",
      host: "dsh",
    });
  });

  it("reads an empty payload when a firing carries nothing either way", () => {
    expect(lifecyclePayload("")).toEqual({});
    expect(lifecyclePayload("", { WENYUAN_HOOK_EVENT: "" })).toEqual({});
  });

  it("reads the first present non-empty string member", () => {
    expect(payloadString({ a: "", b: "b" }, "a", "b")).toBe("b");
    expect(payloadString({ a: 1 }, "a")).toBeUndefined();
    expect(payloadString({}, "missing")).toBeUndefined();
  });
});