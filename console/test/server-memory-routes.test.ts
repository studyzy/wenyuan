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

  it("requires bank_id on the entity collection and does not accept the agent-style alias there", async () => {
    const stub = stubFetch([{ status: 200, body: {} }]);
    restore = stub.restore;
    await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      const accepted = await entities.GET(
        makeRequest("http://localhost/api/entities?bank_id=acme"),
      );
      expect(accepted.status).toBe(200);

      const rejected = await entities.GET(
        makeRequest("http://localhost/api/entities?agent_id=acme"),
      );
      expect(rejected.status).toBe(400);
      expectNoErrorKey(await readJson(rejected));
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

describe("recall and reflect take the bank from the request body", () => {
  it("routes recall to the bank named in the body and normalises a legacy fact type", async () => {
    const stub = stubFetch([{ status: 200, body: { memories: [] } }]);
    restore = stub.restore;
    await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      const response = await recall.POST(
        makeRequest("http://localhost/api/recall", {
          method: "POST",
          body: JSON.stringify({ bank_id: "team/alpha", query: "q", fact_type: "world" }),
        }),
      );

      expect(response.status).toBe(200);
      expect(stub.calls[0]).toContain("/banks/team%2Falpha/memories/recall");
      const forwarded = JSON.parse(stub.bodies[0] ?? "{}");
      expect(forwarded.types).toBe("world");
      expect(forwarded).not.toHaveProperty("fact_type");
      // The budget always reaches the retrieval arms, so an unset one is mid.
      expect(forwarded.budget).toBe("mid");
    });
  });

  it("falls back to the default bank when recall names none", async () => {
    const stub = stubFetch([{ status: 200, body: { memories: [] } }]);
    restore = stub.restore;
    await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      const response = await recall.POST(
        makeRequest("http://localhost/api/recall", {
          method: "POST",
          body: JSON.stringify({ query: "q" }),
        }),
      );

      expect(response.status).toBe(200);
      expect(stub.calls[0]).toContain("/banks/default/memories/recall");
    });
  });

  it("routes reflect to the bank named in the body and keeps the id out of the upstream body", async () => {
    const stub = stubFetch([{ status: 200, body: { answer: "ok" } }]);
    restore = stub.restore;
    await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      const response = await reflect.POST(
        makeRequest("http://localhost/api/reflect", {
          method: "POST",
          body: JSON.stringify({ bank_id: "acme", query: "q" }),
        }),
      );

      expect(response.status).toBe(200);
      expect(stub.calls[0]).toContain("/banks/acme/reflect");
      expect(JSON.parse(stub.bodies[0] ?? "{}")).not.toHaveProperty("bank_id");
    });
  });

  it("falls back to the default bank when reflect names none", async () => {
    const stub = stubFetch([{ status: 200, body: { answer: "ok" } }]);
    restore = stub.restore;
    await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      const response = await reflect.POST(
        makeRequest("http://localhost/api/reflect", {
          method: "POST",
          body: JSON.stringify({ query: "q" }),
        }),
      );

      expect(response.status).toBe(200);
      expect(stub.calls[0]).toContain("/banks/default/reflect");
    });
  });
});