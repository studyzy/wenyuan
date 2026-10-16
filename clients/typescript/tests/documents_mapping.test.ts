/**
 * The document wrappers' mapping, including the one place they translate a
 * response: a missing document reads as `null` rather than a throw, so a caller
 * probing for one does not have to catch a 404.
 */
import { describe, expect, it } from "vitest";

import { WenYuanClient } from "../src/index";
import { call, jsonBody, stubFetch } from "./helpers";

function client() {
  const stub = stubFetch((request) =>
    request.method === "GET" && request.pathname.endsWith("/documents/missing")
      ? { status: 404, json: { detail: "no such document" } }
      : { json: { id: "doc-1", tags: [], items: [], total: 0 } },
  );
  return {
    stub,
    client: new WenYuanClient({ baseUrl: "http://api.test", fetch: stub.fetch }),
  };
}

describe("document mapping", () => {
  it("reads a missing document as null", async () => {
    const { client: api } = client();

    expect(await api.getDocument("bank", "missing")).toBeNull();
  });

  it("reads a document that exists", async () => {
    const { client: api, stub } = client();

    const document = await api.getDocument("bank", "doc-1");

    expect(document?.id).toBe("doc-1");
    expect(call(stub).pathname).toBe("/v1/default/banks/bank/documents/doc-1");
  });

  it("maps the list query and pagination", async () => {
    const { client: api, stub } = client();

    await api.listDocuments("bank", {
      limit: 10,
      offset: 20,
      timeField: "updated_at",
      startDate: "2024-01-01T00:00:00Z",
      endDate: "2024-02-01T00:00:00Z",
    });

    expect(call(stub).pathname).toBe("/v1/default/banks/bank/documents");
    expect(call(stub).query.get("limit")).toBe("10");
    expect(call(stub).query.get("offset")).toBe("20");
    expect(call(stub).query.get("time_field")).toBe("updated_at");
    expect(call(stub).query.get("start_date")).toBe("2024-01-01T00:00:00Z");
    expect(call(stub).query.get("end_date")).toBe("2024-02-01T00:00:00Z");
  });

  it("maps a tag update onto the document's body", async () => {
    const { client: api, stub } = client();

    await api.updateDocument("bank", "doc-1", { tags: ["reviewed"] });

    expect(call(stub).method).toBe("PATCH");
    expect(call(stub).pathname).toBe("/v1/default/banks/bank/documents/doc-1");
    expect(jsonBody(call(stub))).toEqual({ tags: ["reviewed"] });
  });

  it("deletes a document", async () => {
    const { client: api, stub } = client();

    await api.deleteDocument("bank", "doc-1");

    expect(call(stub).method).toBe("DELETE");
    expect(call(stub).pathname).toBe("/v1/default/banks/bank/documents/doc-1");
  });
});