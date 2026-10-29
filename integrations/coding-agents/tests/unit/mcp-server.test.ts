import { describe, expect, it } from "vitest";
import {
  handleMessage,
  parseServerArguments,
  readMessages,
  RECALL_TOOL,
  RECALL_TOOL_NAME,
  SUPPORTED_PROTOCOL_VERSIONS,
} from "../../src/runtime/mcp-server";
import { Readable } from "node:stream";
import type { MemoryClient } from "../../src/runtime/memory-client";

/**
 * The packaged MCP server's protocol side.
 *
 * A host launches the server over standard input/output and speaks JSON-RPC on
 * it, so the server must answer the handshake, refuse to leave a request
 * unanswered, and keep every byte off the wire that is not one JSON document
 * per line. These cases pin those, independently of the memory service.
 */

/** A stand-in memory client: the one read path the server serves. */
function clientReturning(texts: string[]): MemoryClient {
  return {
    async recall() {
      return { ok: true as const, texts };
    },
  } as unknown as MemoryClient;
}

function message(method: string, id: number | null, params?: unknown): { jsonrpc: string; id: number | null; method: string; params?: unknown } {
  return { jsonrpc: "2.0", id, method, ...(params === undefined ? {} : { params }) };
}

describe("the server handshake", () => {
  it("answers an initialize with a supported protocol revision and its own identity", async () => {
    const answer = (await handleMessage(message("initialize", 1, { protocolVersion: "2025-06-18" }), null)) as Record<string, unknown>;
    const result = answer["result"] as Record<string, unknown>;
    expect(result["protocolVersion"]).toBe("2025-06-18");
    expect(result["serverInfo"]).toEqual({ name: "wenyuan-memory", version: "0.1.0" });
  });

  it("answers an unknown requested revision with the newest supported one", async () => {
    const answer = (await handleMessage(message("initialize", 1, { protocolVersion: "nonexistent" }), null)) as Record<string, unknown>;
    const result = answer["result"] as Record<string, unknown>;
    expect(result["protocolVersion"]).toBe(SUPPORTED_PROTOCOL_VERSIONS[SUPPORTED_PROTOCOL_VERSIONS.length - 1]);
  });

  it("carries the request id back on every answer", async () => {
    for (const method of ["initialize", "ping", "tools/list"]) {
      const answer = (await handleMessage(message(method, 42), null)) as Record<string, unknown>;
      expect(answer["id"], method).toBe(42);
    }
  });

  it("writes nothing for a notification, which carries no id", async () => {
    expect(await handleMessage(message("notifications/initialized", null), null)).toBeNull();
    expect(await handleMessage({ jsonrpc: "2.0", method: "notifications/initialized" }, null)).toBeNull();
  });
});

describe("the tool surface", () => {
  it("lists the recall tool with a query input schema", async () => {
    const answer = (await handleMessage(message("tools/list", 2), null)) as Record<string, unknown>;
    const tools = (answer["result"] as Record<string, unknown>)["tools"] as Record<string, unknown>[];
    expect(tools).toEqual([RECALL_TOOL]);
    expect(tools[0]!["name"]).toBe(RECALL_TOOL_NAME);
  });

  it("returns the recalled texts as one text content block", async () => {
    const answer = (await handleMessage(
      message("tools/call", 3, { name: RECALL_TOOL_NAME, arguments: { query: "memory" } }),
      clientReturning(["first", "second"]),
    )) as Record<string, unknown>;
    const result = answer["result"] as Record<string, unknown>;
    expect(result["content"]).toEqual([{ type: "text", text: "first\nsecond" }]);
    expect(result["isError"]).toBeUndefined();
  });

  it("reports an unreachable service rather than failing the request", async () => {
    const answer = (await handleMessage(
      message("tools/call", 4, { name: RECALL_TOOL_NAME, arguments: { query: "memory" } }),
      null,
    )) as Record<string, unknown>;
    const result = answer["result"] as Record<string, unknown>;
    expect(result["content"]).toEqual([{ type: "text", text: "the memory service is not reachable" }]);
  });

  it("answers an unknown tool and a missing query without throwing", async () => {
    const unknown = (await handleMessage(message("tools/call", 5, { name: "nope" }), null)) as Record<string, unknown>;
    expect((unknown["result"] as Record<string, unknown>)["isError"]).toBe(true);
    const missing = (await handleMessage(
      message("tools/call", 6, { name: RECALL_TOOL_NAME, arguments: {} }),
      clientReturning(["x"]),
    )) as Record<string, unknown>;
    expect((missing["result"] as Record<string, unknown>)["content"]).toEqual([
      { type: "text", text: "no query was given" },
    ]);
  });

  it("answers an unknown method with the protocol's method-not-found error", async () => {
    const answer = (await handleMessage(message("no/such/method", 7), null)) as Record<string, unknown>;
    expect((answer["error"] as Record<string, unknown>)["code"]).toBe(-32601);
  });
});

describe("reading and writing the wire", () => {
  it("splits newline-delimited documents and drops blank and malformed lines", async () => {
    const source = [
      JSON.stringify(message("ping", 1)),
      "",
      "not json",
      JSON.stringify(message("ping", 2)),
    ].join("\n");
    const received: unknown[] = [];
    for await (const parsed of readMessages(sourceToStream(source))) {
      received.push(parsed);
    }
    expect(received).toEqual([message("ping", 1), message("ping", 2)]);
  });

  it("ignores a launcher flag it does not know instead of refusing to start", () => {
    expect(parseServerArguments(["--transport", "stdio", "--unknown", "x"])).toEqual({ transport: "stdio" });
    expect(parseServerArguments([])).toEqual({});
  });
});

/** A readable stream over one in-memory string. */
function sourceToStream(source: string): NodeJS.ReadableStream {
  return Readable.from([Buffer.from(source, "utf8")]);
}