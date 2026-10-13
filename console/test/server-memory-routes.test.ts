import { afterEach, describe, expect, it } from "vitest";
import * as listRoute from "../app/api/list/route";
import * as documents from "../app/api/documents/route";
import * as entities from "../app/api/entities/route";
import * as graph from "../app/api/graph/route";
import * as retain from "../app/api/memories/retain/route";
import * as retainAsync from "../app/api/memories/retain_async/route";
import * as memory from "../app/api/memories/[memoryId]/route";
import * as operations from "../app/api/operations/[agentId]/route";
import * as recall from "../app/api/recall/route";
import * as reflect from "../app/api/reflect/route";
import { stubFetch, withEnv } from "./support/fixtures";
import { expectNoErrorKey, makeRequest, readJson } from "./support/harness";

let restore: (() => void) | undefined;
afterEach(() => {
  restore?.();
  restore = undefined;
  delete process.env.HINDSIGHT_CP_DATAPLANE_API_URL;
});

describe("memory-list delete is unsupported", () => {
  it("returns 501 with a localized message", async () => {
    const response = await listRoute.DELETE(makeRequest("http://localhost/api/list?bank_id=acme"));
    expect(response.status).toBe(501);
    const body = await readJson(response);
    expectNoErrorKey(body);
    expect((body.error ?? body.message) as string).not.toBe("errors.unsupportedMemoriesDelete");
  });
});

describe("required query parameters", () => {
  it("returns 400 when document_id is missing", async () => {
    const response = await documents.GET(makeRequest("http://localhost/api/documents"));
    expect(response.status).toBe(400);
    expectNoErrorKey(await readJson(response));
  });

  it("returns 400 when bank_id is missing", async () => {
    const response = await listRoute.GET(makeRequest("http://localhost/api/list"));
    expect(response.status).toBe(400);
    expectNoErrorKey(await readJson(response));
  });

  it("accepts the legacy agent-style alias for bank scoping", async () => {
    const stub = stubFetch([{ status: 200, body: {} }]);
    restore = stub.restore;
    await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      const response = await entities.GET(makeRequest("http://localhost/api/entities?agent_id=acme"));
      expect(response.status).toBe(200);
    });
  });
});

describe("memory route method sets", () => {
  it("binds retain and retain_async as POST", () => {
    expect(typeof retain.POST).toBe("function");
    expect(typeof retainAsync.POST).toBe("function");
  });

  it("binds per-memory read/update", () => {
    expect(typeof memory.GET).toBe("function");
    expect(typeof memory.PATCH).toBe("function");
  });

  it("binds operations get/delete", () => {
    expect(typeof operations.GET).toBe("function");
    expect(typeof operations.DELETE).toBe("function");
    expect(Object.keys(operations).sort()).toEqual(["DELETE", "GET"]);
  });

  it("binds recall, reflect, documents, entities and graph", () => {
    expect(typeof recall.POST).toBe("function");
    expect(typeof reflect.POST).toBe("function");
    expect(typeof documents.GET).toBe("function");
    expect(typeof entities.GET).toBe("function");
    expect(typeof graph.GET).toBe("function");
  });
});