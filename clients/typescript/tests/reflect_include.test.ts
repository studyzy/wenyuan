/**
 * `reflect` forwards its include flags into the ReflectRequest body.
 *
 * A wrapper that never sent `include` would leave the reflect trace
 * (trace.tool_calls / trace.llm_calls) and the based_on facts unreachable from
 * the convenience layer.
 */
import { describe, expect, it } from "vitest";

import { WenYuanClient } from "../src/index";
import { call, jsonBody, stubFetch, type Stub } from "./helpers";

function client() {
  const stub = stubFetch(() => ({ json: { text: "ok" } }));
  return {
    stub,
    client: new WenYuanClient({ baseUrl: "http://api.test", fetch: stub.fetch }),
  };
}

function sentInclude(stub: Stub): unknown {
  return jsonBody(call(stub)).include;
}

describe("reflect include options", () => {
  it("omits include when no include flag is set", async () => {
    const { client: api, stub } = client();

    await api.reflect("bank", "query");

    expect(sentInclude(stub)).toBeUndefined();
  });

  it("sets tool_calls with output on by default", async () => {
    const { client: api, stub } = client();

    await api.reflect("bank", "query", { includeToolCalls: true });

    expect(sentInclude(stub)).toEqual({ tool_calls: { output: true } });
  });

  it("requests an inputs-only trace when includeToolCallOutput is false", async () => {
    const { client: api, stub } = client();

    await api.reflect("bank", "query", { includeToolCalls: true, includeToolCallOutput: false });

    expect(sentInclude(stub)).toEqual({ tool_calls: { output: false } });
  });

  it("sets facts for includeFacts", async () => {
    const { client: api, stub } = client();

    await api.reflect("bank", "query", { includeFacts: true });

    expect(sentInclude(stub)).toEqual({ facts: {} });
  });

  it("combines includeFacts and includeToolCalls", async () => {
    const { client: api, stub } = client();

    await api.reflect("bank", "query", { includeFacts: true, includeToolCalls: true });

    expect(sentInclude(stub)).toEqual({ facts: {}, tool_calls: { output: true } });
  });

  it("treats includeToolCallOutput as a no-op without includeToolCalls", async () => {
    const { client: api, stub } = client();

    await api.reflect("bank", "query", { includeToolCallOutput: false });

    expect(sentInclude(stub)).toBeUndefined();
  });

  it("defaults the reflect budget to low", async () => {
    const { client: api, stub } = client();

    await api.reflect("bank", "query");

    expect(jsonBody(call(stub)).budget).toBe("low");
  });

  it("posts the query to the bank's reflect route", async () => {
    const { client: api, stub } = client();

    await api.reflect("bank", "why?");

    expect(call(stub).method).toBe("POST");
    expect(call(stub).path).toBe("/v1/default/banks/bank/reflect");
    expect(jsonBody(call(stub)).query).toBe("why?");
  });
});