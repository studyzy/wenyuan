/**
 * tag_groups forwarding in the hand-written wrapper.
 *
 * Pins that a `tag_groups` filter reaches the request body with the leaf's
 * `resolve` field intact, so fuzzy tag matching is usable through the wrapper
 * and not only over raw HTTP.
 */
import { describe, expect, it } from "vitest";

import { WenYuanClient } from "../src/index";
import { call, jsonBody, stubFetch } from "./helpers";

const FUZZY_GROUP = {
  tags: ["typsecript"],
  match: "any_strict" as const,
  resolve: "fuzzy" as const,
};

function client() {
  const stub = stubFetch(() => ({ json: { results: [], text: "" } }));
  return {
    stub,
    client: new WenYuanClient({ baseUrl: "http://api.test", fetch: stub.fetch }),
  };
}

describe("fuzzy tag_groups mapping", () => {
  it("recall forwards a fuzzy tag group with resolve intact", async () => {
    const { client: api, stub } = client();

    await api.recall("bank-1", "what language is the parser in", {
      tagGroups: [{ ...FUZZY_GROUP }],
    });

    expect(jsonBody(call(stub)).tag_groups).toEqual([FUZZY_GROUP]);
  });

  it("reflect forwards a fuzzy tag group with resolve intact", async () => {
    const { client: api, stub } = client();

    await api.reflect("bank-1", "what language is the parser in", {
      tagGroups: [{ ...FUZZY_GROUP }],
    });

    expect(jsonBody(call(stub)).tag_groups).toEqual([FUZZY_GROUP]);
  });

  it("maps recall's budget, windows and include flags", async () => {
    const { client: api, stub } = client();

    await api.recall("bank-1", "what happened", {
      maxTokens: 512,
      minScores: { semantic: 0.2, final: 0.5 },
      temporalWindow: { start: "2024-01-01T00:00:00Z", end: "2024-02-01T00:00:00Z" },
      includeEntities: false,
      includeChunks: true,
      maxChunkTokens: 1024,
      includeSourceFacts: true,
      preferObservations: true,
      queryTimestamp: "2024-03-01T09:00:00Z",
      trace: true,
    });

    const body = jsonBody(call(stub));
    expect(body.max_tokens).toBe(512);
    expect(body.min_scores).toEqual({ semantic: 0.2, final: 0.5 });
    expect(body.temporal_window).toEqual({
      start: "2024-01-01T00:00:00Z",
      end: "2024-02-01T00:00:00Z",
    });
    expect(body.include).toEqual({
      entities: null,
      chunks: { max_tokens: 1024 },
      source_facts: { max_tokens: 4096 },
    });
    expect(body.prefer_observations).toBe(true);
    expect(body.query_timestamp).toBe("2024-03-01T09:00:00Z");
    expect(body.trace).toBe(true);
  });

  it("defaults recall's budget to mid and leaves include unset", async () => {
    const { client: api, stub } = client();

    await api.recall("bank-1", "what happened");

    const body = jsonBody(call(stub));
    expect(body.budget).toBe("mid");
    expect(body.include).toEqual({
      entities: undefined,
      chunks: undefined,
      source_facts: undefined,
    });
  });

  it("applies the documented include defaults", async () => {
    const { client: api, stub } = client();

    await api.recall("bank-1", "what happened", {
      includeEntities: true,
      includeChunks: true,
    });

    expect(jsonBody(call(stub)).include).toEqual({
      entities: { max_tokens: 500 },
      chunks: { max_tokens: 8192 },
      source_facts: undefined,
    });
  });
});