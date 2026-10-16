/**
 * The list time window's request mapping.
 *
 * Both wrappers enumerate query parameters rather than passing a dict through,
 * so one added to the API but not to a wrapper is silently dropped for every
 * consumer of that language, and nothing but this test stands behind a GET
 * query parameter.
 */
import { describe, expect, it } from "vitest";

import { WenYuanClient } from "../src/index";
import { call, stubFetch } from "./helpers";

/** A client whose list calls answer with an empty page. */
function client() {
  const stub = stubFetch(() => ({ json: { items: [], total: 0, limit: 100, offset: 0 } }));
  return {
    stub,
    client: new WenYuanClient({ baseUrl: "http://api.test", fetch: stub.fetch }),
  };
}

describe("list time window mapping", () => {
  it("maps the window onto the query", async () => {
    const { client: api, stub } = client();

    await api.listMemories("bank", {
      timeField: "mentioned_at",
      startDate: "2024-01-01T00:00:00Z",
      endDate: "2024-02-01T00:00:00Z",
    });

    expect(call(stub).path).toBe(
      "/v1/default/banks/bank/memories/list?time_field=mentioned_at&start_date=2024-01-01T00%3A00%3A00Z&end_date=2024-02-01T00%3A00%3A00Z",
    );
    expect(call(stub).query.get("time_field")).toBe("mentioned_at");
    expect(call(stub).query.get("start_date")).toBe("2024-01-01T00:00:00Z");
    expect(call(stub).query.get("end_date")).toBe("2024-02-01T00:00:00Z");
  });

  it("maps the tag filter onto the query", async () => {
    const { client: api, stub } = client();

    await api.listMemories("bank", { tags: ["user:dan"], tagsMatch: "any_strict" });

    expect(call(stub).query.getAll("tags")).toEqual(["user:dan"]);
    expect(call(stub).query.get("tags_match")).toBe("any_strict");
  });

  it("maps the documents window onto the query", async () => {
    const { client: api, stub } = client();

    await api.listDocuments("bank", {
      timeField: "created_at",
      startDate: "2024-01-01T00:00:00Z",
    });

    expect(call(stub).path).toBe(
      "/v1/default/banks/bank/documents?time_field=created_at&start_date=2024-01-01T00%3A00%3A00Z",
    );
  });

  it("leaves the window off entirely unless it is asked for", async () => {
    const { client: api, stub } = client();

    await api.listMemories("bank");

    // Undefined rather than a default axis: the endpoint keeps its own ordering
    // and drops nothing unless a caller asks for a window.
    expect([...call(stub).query.keys()]).toEqual([]);
  });
});