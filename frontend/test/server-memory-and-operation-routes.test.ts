import { afterEach, describe, expect, it } from "vitest";
import * as documentChunks from "../app/api/documents/chunks/route";
import * as documentReprocess from "../app/api/documents/reprocess/route";
import * as documentTransfer from "../app/api/documents/transfer/route";
import * as entity from "../app/api/entities/[entityId]/route";
import * as entityRegenerate from "../app/api/entities/[entityId]/regenerate/route";
import * as entityGraph from "../app/api/entities/graph/route";
import * as memoryHistory from "../app/api/memories/[memoryId]/history/route";
import * as memory from "../app/api/memories/[memoryId]/route";
import * as listRoute from "../app/api/list/route";
import * as reflect from "../app/api/reflect/route";
import * as kbPage from "../app/api/knowledge-base/pages/[pageId]/route";
import * as timeseries from "../app/api/stats/[agentId]/memories-timeseries/route";
import { stubFetch, withEnv } from "./support/fixtures";
import { expectNoErrorKey, makeRequest, readJson } from "./support/harness";

let restore: (() => void) | undefined;
afterEach(() => {
  restore?.();
  restore = undefined;
  delete process.env.WENYUAN_CP_DATAPLANE_API_URL;
});

describe("memory and operation route method sets", () => {
  it("binds document children", () => {
    expect(Object.keys(documentChunks).sort()).toEqual(["GET"]);
    expect(Object.keys(documentReprocess).sort()).toEqual(["POST"]);
    expect(Object.keys(documentTransfer).sort()).toEqual(["GET", "POST"]);
  });

  it("binds entity children", () => {
    expect(Object.keys(entity).sort()).toEqual(["GET"]);
    expect(Object.keys(entityRegenerate).sort()).toEqual(["POST"]);
    expect(Object.keys(entityGraph).sort()).toEqual(["GET"]);
  });

  it("binds memory history, knowledge-base page and stats timeseries as GET", () => {
    expect(Object.keys(memoryHistory).sort()).toEqual(["GET"]);
    expect(Object.keys(kbPage).sort()).toEqual(["GET"]);
    expect(Object.keys(timeseries).sort()).toEqual(["GET"]);
  });
});

describe("required query parameters", () => {
  it("returns 400 when document_id is missing on chunks and reprocess", async () => {
    const missingChunks = await documentChunks.GET(makeRequest("http://localhost/api/documents/chunks"));
    expect(missingChunks.status).toBe(400);
    expectNoErrorKey(await readJson(missingChunks));

    const missingReprocess = await documentReprocess.POST(
      makeRequest("http://localhost/api/documents/reprocess", { method: "POST", body: "{}" }),
    );
    expect(missingReprocess.status).toBe(400);
  });

  it("returns 400 when bank_id is missing on entity and timeseries routes", async () => {
    const missingEntity = await entityGraph.GET(makeRequest("http://localhost/api/entities/graph"));
    expect(missingEntity.status).toBe(400);

    // The timeseries route takes its bank id from the path segment, so it is
    // rejected only when that segment is empty, not by an absent query param.
    const missingTimeseries = await timeseries.GET(
      makeRequest("http://localhost/api/stats/acme/memories-timeseries"),
      { params: Promise.resolve({ agentId: "" }) },
    );
    expect(missingTimeseries.status).toBe(400);
    expectNoErrorKey(await readJson(missingTimeseries));
  });
});

describe("document member sub-resources travel in the path", () => {
  it("carries a slashed document id in the encoded member path", async () => {
    const stub = stubFetch([{ status: 200, body: {} }]);
    restore = stub.restore;
    await withEnv({ WENYUAN_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      await documentChunks.GET(
        makeRequest("http://localhost/api/documents/chunks?document_id=dir%2Ffile.md&bank_id=acme"),
      );
      expect(stub.calls[0]).toContain("/v1/default/banks/acme/documents/dir%2Ffile.md/chunks");
      expect(stub.calls[0]).not.toContain("document_id=");
    });
  });
});

describe("entity id decoding", () => {
  it("decodes the entity id segment before forwarding upstream", async () => {
    const stub = stubFetch([{ status: 200, body: {} }]);
    restore = stub.restore;
    await withEnv({ WENYUAN_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      await entity.GET(makeRequest("http://localhost/api/entities/a%2Fb?bank_id=acme"), {
        params: Promise.resolve({ entityId: "a%2Fb" }),
      });
      expect(stub.calls[0]).toBe("http://upstream.local/v1/default/banks/acme/entities/a%2Fb");
    });
  });
});

describe("memory history 404 translation", () => {
  it("maps an upstream 404 to a localized 404", async () => {
    const stub = stubFetch([{ status: 404, body: { detail: "missing" } }]);
    restore = stub.restore;
    await withEnv({ WENYUAN_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      const response = await memoryHistory.GET(
        makeRequest("http://localhost/api/memories/mem1/history?bank_id=acme"),
        { params: Promise.resolve({ memoryId: "mem1" }) },
      );
      expect(response.status).toBe(404);
      expectNoErrorKey(await readJson(response));
    });
  });

  it("maps another upstream failure to a localized 500", async () => {
    const stub = stubFetch([{ status: 500, body: { detail: "boom" } }]);
    restore = stub.restore;
    await withEnv({ WENYUAN_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      const response = await memoryHistory.GET(
        makeRequest("http://localhost/api/memories/mem1/history?bank_id=acme"),
        { params: Promise.resolve({ memoryId: "mem1" }) },
      );
      expect(response.status).toBe(500);
    });
  });
});

describe("document transfer export orchestration", () => {
  it("submits the export, polls the operation, then streams the archive", async () => {
    const stub = stubFetch([
      { status: 200, body: { operation_id: "op1" } },
      { status: 200, body: { status: "completed", result_metadata: { download_url: "/v1/default/files/archive.zip" } } },
      { status: 200, body: "zip-bytes", contentType: "application/zip" },
    ]);
    restore = stub.restore;
    await withEnv({ WENYUAN_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      const response = await documentTransfer.GET(
        makeRequest("http://localhost/api/documents/transfer?bank_id=acme"),
      );
      expect(response.status).toBe(200);
      expect(response.headers.get("content-type")).toBe("application/zip");
      expect(await response.text()).toBe("zip-bytes");
    });
  });
});

describe("stats timeseries forwards its documented parameters", () => {
  it("forwards period and time-field query parameters", async () => {
    const stub = stubFetch([{ status: 200, body: {} }]);
    restore = stub.restore;
    await withEnv({ WENYUAN_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      await timeseries.GET(
        makeRequest(
          "http://localhost/api/stats/acme/memories-timeseries?period=30d&undocumented=1",
        ),
        { params: Promise.resolve({ agentId: "acme" }) },
      );
      expect(stub.calls[0]).toContain("/v1/default/banks/acme/stats/memories-timeseries");
      expect(stub.calls[0]).toContain("period=30d");
      expect(stub.calls[0]).not.toContain("undocumented");
    });
  });
});
describe("memory PATCH routing follows the contract", () => {
  it("takes the bank id from the body and excludes it from the forwarded body", async () => {
    const stub = stubFetch([{ status: 200, body: {} }]);
    restore = stub.restore;
    await withEnv({ WENYUAN_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      const response = await memory.PATCH(
        makeRequest("http://localhost/api/memories/m1", {
          method: "PATCH",
          body: JSON.stringify({ bank_id: "acme", text: "updated", state: "active" }),
        }),
        { params: Promise.resolve({ memoryId: "m1" }) },
      );
      expect(response.status).toBe(200);
      const forwarded = JSON.parse(stub.bodies[0] ?? "{}");
      expect(forwarded).not.toHaveProperty("bank_id");
      expect(forwarded).toMatchObject({ text: "updated", state: "active" });
    });
  });

  it("returns 400 on an unparseable body", async () => {
    const stub = stubFetch([{ status: 200, body: {} }]);
    restore = stub.restore;
    await withEnv({ WENYUAN_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      const response = await memory.PATCH(
        makeRequest("http://localhost/api/memories/m1", { method: "PATCH", body: "{not json" }),
        { params: Promise.resolve({ memoryId: "m1" }) },
      );
      expect(response.status).toBe(400);
      expectNoErrorKey(await readJson(response));
    });
  });
});

describe("list DELETE is always unsupported", () => {
  it("returns 501 without contacting the upstream", async () => {
    const stub = stubFetch([{ status: 200, body: {} }]);
    restore = stub.restore;
    const response = await listRoute.DELETE(makeRequest("http://localhost/api/list?bank_id=acme"));
    expect(response.status).toBe(501);
    expect(stub.calls.length).toBe(0);
  });
});

describe("reflect translates the legacy include booleans", () => {
  it("builds an include object and defaults the budget", async () => {
    const stub = stubFetch([{ status: 200, body: {} }]);
    restore = stub.restore;
    await withEnv({ WENYUAN_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      await reflect.POST(
        makeRequest("http://localhost/api/reflect?bank_id=acme", {
          method: "POST",
          body: JSON.stringify({ query: "q", thinking_budget: 10, include_facts: true, include_tool_calls: true }),
        }),
      );
      const forwarded = JSON.parse(stub.bodies[0] ?? "{}");
      expect(forwarded.budget).toBe("mid");
      expect(forwarded.include).toMatchObject({ facts: {}, tool_calls: {} });
      expect(forwarded).not.toHaveProperty("include_facts");
      expect(forwarded).not.toHaveProperty("thinking_budget");
    });
  });

  it("defaults the budget to low with no thinking budget and omits include", async () => {
    const stub = stubFetch([{ status: 200, body: {} }]);
    restore = stub.restore;
    await withEnv({ WENYUAN_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      await reflect.POST(
        makeRequest("http://localhost/api/reflect?bank_id=acme", {
          method: "POST",
          body: JSON.stringify({ query: "q" }),
        }),
      );
      const forwarded = JSON.parse(stub.bodies[0] ?? "{}");
      expect(forwarded.budget).toBe("low");
      expect(forwarded).not.toHaveProperty("include");
    });
  });
});
