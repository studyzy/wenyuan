/**
 * Mental-model query forwarding in the hand-written wrapper.
 *
 * A regression here would silently restore a `detail=full` default or drop list
 * pagination and tag-match controls.
 */
import { describe, expect, it } from "vitest";

import { WenYuanClient } from "../src/index";
import { call, stubFetch } from "./helpers";

function client() {
  const stub = stubFetch(() => ({ json: { items: [], total: 0 } }));
  return {
    stub,
    client: new WenYuanClient({ baseUrl: "http://api.test", fetch: stub.fetch }),
  };
}

describe("mental-model query mapping", () => {
  it("forwards every supported list query option", async () => {
    const { client: api, stub } = client();

    await api.listMentalModels("bank-1", {
      tags: ["project"],
      tagsMatch: "exact",
      detail: "metadata",
      limit: 25,
      offset: 50,
    });

    expect(call(stub).path).toBe(
      "/v1/default/banks/bank-1/mental-models?tags=project&tags_match=exact&detail=metadata&limit=25&offset=50",
    );
  });

  it("preserves the optionless and tags-only list request shapes", async () => {
    const { client: api, stub } = client();

    await api.listMentalModels("bank-1");
    expect([...call(stub, 0).query.keys()]).toEqual([]);

    await api.listMentalModels("bank-1", { tags: ["project"] });
    expect(call(stub, 1).query.getAll("tags")).toEqual(["project"]);
  });

  it("forwards detail when fetching one mental model", async () => {
    const { client: api, stub } = client();

    await api.getMentalModel("bank-1", "model-1", { detail: "content" });

    expect(call(stub).path).toBe("/v1/default/banks/bank-1/mental-models/model-1?detail=content");
  });

  it("preserves the optionless get request shape", async () => {
    const { client: api, stub } = client();

    await api.getMentalModel("bank-1", "model-1");

    expect(call(stub).path).toBe("/v1/default/banks/bank-1/mental-models/model-1");
    expect(call(stub).query.toString()).toBe("");
  });

  it("posts a refresh to the model's refresh route", async () => {
    const { client: api, stub } = client();

    await api.refreshMentalModel("bank-1", "model-1");

    expect(call(stub).method).toBe("POST");
    expect(call(stub).path).toBe("/v1/default/banks/bank-1/mental-models/model-1/refresh");
  });

  it("reads a mental model's history", async () => {
    const { client: api, stub } = client();

    await api.getMentalModelHistory("bank-1", "model-1");

    expect(call(stub).path).toBe("/v1/default/banks/bank-1/mental-models/model-1/history");
  });
});