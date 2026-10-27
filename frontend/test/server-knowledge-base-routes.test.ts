import { describe, expect, it } from "vitest";
import * as tree from "../app/api/knowledge-base/tree/route";
import * as nodes from "../app/api/knowledge-base/nodes/[nodeId]/route";
import * as pages from "../app/api/knowledge-base/pages/route";
import * as search from "../app/api/knowledge-base/search/route";
import * as folders from "../app/api/knowledge-base/folders/route";
import * as exportRoute from "../app/api/knowledge-base/export/route";
import { stubFetch, withEnv } from "./support/fixtures";
import { makeRequest, readJson } from "./support/harness";

const GROUPS: Array<[string, Record<string, unknown>]> = [
  ["tree", tree as unknown as Record<string, unknown>],
  ["nodes", nodes as unknown as Record<string, unknown>],
  ["pages", pages as unknown as Record<string, unknown>],
  ["search", search as unknown as Record<string, unknown>],
  ["folders", folders as unknown as Record<string, unknown>],
  ["export", exportRoute as unknown as Record<string, unknown>],
];

describe("knowledge-base route method sets", () => {
  it("binds at least one method per knowledge-base path", () => {
    for (const [name, group] of GROUPS) {
      const methods = Object.values(group).filter((value) => typeof value === "function");
      expect(methods.length, name).toBeGreaterThan(0);
    }
  });
});

describe("knowledge-base upstream passthrough", () => {
  it("proxies the tree route and forwards the upstream status and payload", async () => {
    const stub = stubFetch([{ status: 200, body: { nodes: [] } }]);
    const restore = stub.restore;
    try {
      await withEnv({ WENYUAN_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
        const response = await tree.GET(makeRequest("http://localhost/api/knowledge-base/tree?bank_id=acme"));
        expect(response.status).toBe(200);
        expect(await readJson(response)).toEqual({ nodes: [] });
        expect(stub.calls[0]).toContain("/v1/default/banks/acme/knowledge-base/tree");
      });
    } finally {
      restore();
    }
  });
});
describe("knowledge-base routes are bank-scoped and require bank_id", () => {
  it("rejects a missing bank id on every read route", async () => {
    for (const [label, call] of [
      ["tree", () => tree.GET(makeRequest("http://localhost/api/knowledge-base/tree"))],
      ["search", () => search.GET(makeRequest("http://localhost/api/knowledge-base/search"))],
      ["export", () => exportRoute.GET(makeRequest("http://localhost/api/knowledge-base/export"))],
      [
        "node patch",
        () =>
          nodes.PATCH(makeRequest("http://localhost/api/knowledge-base/nodes/n1", { method: "PATCH", body: "{}" }), {
            params: Promise.resolve({ nodeId: "n1" }),
          }),
      ],
      [
        "folder create",
        () => folders.POST(makeRequest("http://localhost/api/knowledge-base/folders", { method: "POST", body: "{}" })),
      ],
      [
        "page save",
        () => pages.POST(makeRequest("http://localhost/api/knowledge-base/pages", { method: "POST", body: "{}" })),
      ],
    ] as const) {
      const response = await call();
      expect(response.status, `${label} should require bank_id`).toBe(400);
      expect(await readJson(response)).not.toHaveProperty("errorKey");
    }
  });

  it("short-circuits a search with no query term to an empty result set", async () => {
    const stub = stubFetch([{ status: 200, body: { results: [{ id: "x" }], total: 1 } }]);
    const restore = stub.restore;
    try {
      await withEnv({ WENYUAN_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
        const response = await search.GET(
          makeRequest("http://localhost/api/knowledge-base/search?bank_id=acme"),
        );
        expect(response.status).toBe(200);
        expect(await readJson(response)).toEqual({ results: [], total: 0 });
        expect(stub.calls.length, "no upstream call for an empty query").toBe(0);
      });
    } finally {
      restore();
    }
  });

  it("defaults the search limit to ten when the caller omits it", async () => {
    const stub = stubFetch([{ status: 200, body: {} }]);
    const restore = stub.restore;
    try {
      await withEnv({ WENYUAN_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
        await search.GET(makeRequest("http://localhost/api/knowledge-base/search?bank_id=acme&q=hello"));
        expect(stub.calls[0]).toContain("q=hello");
        expect(stub.calls[0]).toContain("limit=10");
      });
    } finally {
      restore();
    }
  });
});
