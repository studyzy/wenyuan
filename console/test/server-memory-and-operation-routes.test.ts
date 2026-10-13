import { afterEach, describe, expect, it } from "vitest";
import * as documentChunks from "../app/api/documents/chunks/route";
import * as documentReprocess from "../app/api/documents/reprocess/route";
import * as documentTransfer from "../app/api/documents/transfer/route";
import * as entity from "../app/api/entities/[entityId]/route";
import * as entityRegenerate from "../app/api/entities/[entityId]/regenerate/route";
import * as entityGraph from "../app/api/entities/graph/route";
import * as memoryHistory from "../app/api/memories/[memoryId]/history/route";
import * as kbPage from "../app/api/knowledge-base/pages/[pageId]/route";
import * as timeseries from "../app/api/stats/[agentId]/memories-timeseries/route";
import { stubFetch, withEnv } from "./support/fixtures";
import { expectNoErrorKey, makeRequest, readJson } from "./support/harness";

let restore: (() => void) | undefined;
afterEach(() => {
  restore?.();
  restore = undefined;
  delete process.env.HINDSIGHT_CP_DATAPLANE_API_URL;
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

    const missingTimeseries = await timeseries.GET(
      makeRequest("http://localhost/api/stats/acme/memories-timeseries"),
      { params: Promise.resolve({ agentId: "acme" }) },
    );
    expect(missingTimeseries.status).toBe(400);
  });
});

describe("document and chunk identifiers travel in the query string", () => {
  it("carries a slashed document id in the query string", async () => {
    const stub = stubFetch([{ status: 200, body: {} }]);
    restore = stub.restore;
    await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      await documentChunks.GET(
        makeRequest("http://localhost/api/documents/chunks?document_id=dir%2Ffile.md&bank_id=acme"),
      );
      expect(stub.calls[0]).toContain("/v1/default/documents/chunks");
      expect(stub.calls[0]).toContain("document_id=dir%2Ffile.md");
    });
  });
});

describe("entity id decoding", () => {
  it("decodes the entity id segment before forwarding upstream", async () => {
    const stub = stubFetch([{ status: 200, body: {} }]);
    restore = stub.restore;
    await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      await entity.GET(makeRequest("http://localhost/api/entities/a%2Fb?bank_id=acme"), {
        params: Promise.resolve({ entityId: "a%2Fb" }),
      });
      expect(stub.calls[0]).toBe("http://upstream.local/v1/default/entities/a%2Fb?bank_id=acme");
    });
  });
});

describe("memory history 404 translation", () => {
  it("maps an upstream 404 to a localized 404", async () => {
    const stub = stubFetch([{ status: 404, body: { detail: "missing" } }]);
    restore = stub.restore;
    await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
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
    await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
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
      { status: 200, body: { status: "completed", result: { download_url: "/v1/default/files/archive.zip" } } },
      { status: 200, body: "zip-bytes", contentType: "application/zip" },
    ]);
    restore = stub.restore;
    await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      const response = await documentTransfer.GET(
        makeRequest("http://localhost/api/documents/transfer?bank_id=acme"),
      );
      expect(response.status).toBe(200);
      expect(response.headers.get("content-type")).toBe("application/zip");
      expect(await response.text()).toBe("zip-bytes");
    });
  });
});

describe("stats timeseries accepts period and time-field parameters", () => {
  it("forwards period and time-field query parameters", async () => {
    const stub = stubFetch([{ status: 200, body: {} }]);
    restore = stub.restore;
    await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      await timeseries.GET(
        makeRequest("http://localhost/api/stats/acme/memories-timeseries?bank_id=acme&period=30d&time_field=created_at"),
        { params: Promise.resolve({ agentId: "acme" }) },
      );
      expect(stub.calls[0]).toContain("/v1/default/stats/acme/memories-timeseries");
      expect(stub.calls[0]).toContain("period=30d");
      expect(stub.calls[0]).toContain("time_field=created_at");
    });
  });
});