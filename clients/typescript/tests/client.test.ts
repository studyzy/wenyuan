import { describe, expect, it } from "vitest";

import {
  DEFAULT_BASE_URL,
  DEFAULT_USER_AGENT,
  createClient,
  recall,
  reflect,
  resolveBaseUrl,
  retain,
} from "../src/index";

/** One recorded call to the stubbed fetch. */
interface Call {
  url: string;
  method: string;
  headers: Record<string, string>;
  body: unknown;
}

/** A stubbed `fetch` that records the request and replays `payload` as JSON. */
function stubFetch(payload: unknown, status = 200): { calls: Call[]; fetch: typeof fetch } {
  const calls: Call[] = [];
  const impl: typeof fetch = async (input, init) => {
    const request = input instanceof Request ? input : new Request(input, init);
    const text = await request.text();
    calls.push({
      url: request.url,
      method: request.method,
      headers: Object.fromEntries(request.headers.entries()),
      body: text === "" ? undefined : JSON.parse(text),
    });
    return new Response(JSON.stringify(payload), {
      status,
      headers: { "content-type": "application/json" },
    });
  };
  return { calls, fetch: impl };
}

describe("resolveBaseUrl", () => {
  it("prefers the explicit option and trims trailing slashes", () => {
    expect(resolveBaseUrl("http://example.test:9999///")).toBe("http://example.test:9999");
  });

  it("falls back to the documented default", () => {
    const previous = process.env.HINDSIGHT_API_URL;
    delete process.env.HINDSIGHT_API_URL;
    try {
      expect(resolveBaseUrl()).toBe(DEFAULT_BASE_URL);
    } finally {
      if (previous !== undefined) process.env.HINDSIGHT_API_URL = previous;
    }
  });

  it("reads the deployment's environment variable when no option is given", () => {
    const previous = process.env.HINDSIGHT_API_URL;
    process.env.HINDSIGHT_API_URL = "http://env.test:1234/";
    try {
      expect(resolveBaseUrl()).toBe("http://env.test:1234");
    } finally {
      if (previous === undefined) delete process.env.HINDSIGHT_API_URL;
      else process.env.HINDSIGHT_API_URL = previous;
    }
  });
});

describe("retain", () => {
  it("posts the item to the bank's memories endpoint and decodes the reply", async () => {
    const { calls, fetch } = stubFetch({
      success: true,
      bank_id: "alice",
      items_count: 1,
      async: false,
    });
    const client = createClient({ baseUrl: "http://api.test", fetch, apiKey: "secret" });

    const response = await retain(
      client,
      "alice",
      { content: "Alice works at the observatory.", context: "onboarding" },
      { async: false },
    );

    expect(response.items_count).toBe(1);
    expect(calls).toHaveLength(1);
    expect(calls[0]?.method).toBe("POST");
    expect(calls[0]?.url).toBe("http://api.test/v1/default/banks/alice/memories");
    expect(calls[0]?.body).toEqual({
      async: false,
      items: [{ content: "Alice works at the observatory.", context: "onboarding" }],
    });
    expect(calls[0]?.headers["authorization"]).toBe("Bearer secret");
    expect(calls[0]?.headers["user-agent"]).toBe(DEFAULT_USER_AGENT);
  });

  it("accepts full memory items and rejects an empty list before calling out", async () => {
    const { calls, fetch } = stubFetch({ success: true, bank_id: "b", items_count: 2 });
    const client = createClient({ baseUrl: "http://api.test", fetch });

    await retain(client, "b", [
      "plain text",
      { content: "tagged", metadata: { source: "chat" } },
    ]);

    expect(calls[0]?.body).toEqual({
      items: [
        { content: "plain text" },
        { content: "tagged", metadata: { source: "chat" } },
      ],
    });
    expect(() => retain(client, "b", [])).toThrow(TypeError);
    expect(calls).toHaveLength(1);
  });
});

describe("recall", () => {
  it("posts the query and returns the resolved payload", async () => {
    const { calls, fetch } = stubFetch({ results: [{ content: "Alice works at the observatory." }] });
    const client = createClient({ baseUrl: "http://api.test/", fetch });

    const response = await recall(client, "alice", "Where does Alice work?", { budget: "low" });

    expect(response.results).toHaveLength(1);
    expect(calls[0]?.method).toBe("POST");
    expect(calls[0]?.url).toBe("http://api.test/v1/default/banks/alice/memories/recall");
    expect(calls[0]?.body).toEqual({ budget: "low", query: "Where does Alice work?" });
  });

  it("escalates a server error to a typed throw", async () => {
    const { fetch } = stubFetch({ detail: "boom" }, 500);
    const client = createClient({ baseUrl: "http://api.test", fetch });

    await expect(recall(client, "alice", "anything")).rejects.toThrow(/status 500/);
  });
});

describe("reflect", () => {
  it("posts the query to the reflect endpoint", async () => {
    const { calls, fetch } = stubFetch({ text: "Alice observes stars." });
    const client = createClient({ baseUrl: "http://api.test", fetch });

    const response = await reflect(client, "alice", "What does Alice do?", { max_tokens: 64 });

    expect(response.text).toBe("Alice observes stars.");
    expect(calls[0]?.url).toBe("http://api.test/v1/default/banks/alice/reflect");
    expect(calls[0]?.body).toEqual({ max_tokens: 64, query: "What does Alice do?" });
  });
});