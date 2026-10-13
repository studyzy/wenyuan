import { describe, expect, it } from "vitest";
import * as stats from "../app/api/stats/[agentId]/route";
import * as chunks from "../app/api/chunks/route";
import * as filesRetain from "../app/api/files/retain/route";
import * as filesDownload from "../app/api/files/download/route";
import { stubFetch, withEnv } from "./support/fixtures";
import { expectNoErrorKey, makeRequest, readJson } from "./support/harness";

describe("file download SSRF guard", () => {
  it("returns 400 and performs no upstream fetch for a disallowed path", async () => {
    const stub = stubFetch([{ status: 200, body: {} }]);
    const restore = stub.restore;
    try {
      await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
        const response = await filesDownload.GET(
          makeRequest("http://localhost/api/files/download?path=/etc/passwd"),
        );
        expect(response.status).toBe(400);
        expectNoErrorKey(await readJson(response));
        expect(stub.calls).toHaveLength(0);
      });
    } finally {
      restore();
    }
  });

  it("proxies an allowed upstream download path", async () => {
    const stub = stubFetch([{ status: 200, body: "binary", contentType: "application/octet-stream" }]);
    const restore = stub.restore;
    try {
      await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
        const response = await filesDownload.GET(
          makeRequest("http://localhost/api/files/download?path=/v1/default/files/download/a.txt"),
        );
        expect(response.status).toBe(200);
        expect(stub.calls[0]).toContain("/v1/default/files/download/a.txt");
      });
    } finally {
      restore();
    }
  });
});

describe("chunk scoping parameter", () => {
  it("returns 400 when chunk_id is missing", async () => {
    const response = await chunks.GET(makeRequest("http://localhost/api/chunks"));
    expect(response.status).toBe(400);
    expectNoErrorKey(await readJson(response));
  });
});

describe("stats and files method sets", () => {
  it("binds stats, chunks, files retain and files download", () => {
    expect(typeof stats.GET).toBe("function");
    expect(typeof chunks.GET).toBe("function");
    expect(typeof filesRetain.POST).toBe("function");
    expect(typeof filesDownload.GET).toBe("function");
  });
});