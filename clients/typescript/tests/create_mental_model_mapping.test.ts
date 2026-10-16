/**
 * The createMentalModel wrapper's trigger mapping.
 *
 * These need no server: the request the generated client would send is recorded,
 * so the camelCase options can be checked against the snake_case body. A wrapper
 * that dropped most trigger fields would leave a caller unable to set tags_match
 * at all, with nothing in the response to say the option was ignored.
 */
import { describe, expect, it } from "vitest";

import { WenYuanClient } from "../src/index";
import { call, jsonBody, jsonObject, stubFetch, type Stub } from "./helpers";

function client() {
  const stub = stubFetch(() => ({
    json: { mental_model_id: "mm-1", operation_id: "op-1" },
  }));
  return {
    stub,
    client: new WenYuanClient({ baseUrl: "http://api.test", fetch: stub.fetch }),
  };
}

/** The body of the first (only) create call. */
function sentBody(stub: Stub): Record<string, unknown> {
  return jsonBody(call(stub));
}

/** The trigger of the first (only) create call. */
function sentTrigger(stub: Stub): Record<string, unknown> {
  return jsonObject(sentBody(stub).trigger, "trigger");
}

describe("createMentalModel trigger mapping", () => {
  it("threads tagsMatch into trigger.tags_match", async () => {
    const { client: api, stub } = client();

    await api.createMentalModel("bank", "Projects", "Which projects?", {
      tags: ["projects", "mental-model"],
      trigger: { tagsMatch: "any" },
    });

    expect(sentBody(stub).tags).toEqual(["projects", "mental-model"]);
    expect(sentTrigger(stub).tags_match).toBe("any");
  });

  it("threads tagGroups into trigger.tag_groups", async () => {
    const { client: api, stub } = client();

    await api.createMentalModel("bank", "Scoped", "q", {
      trigger: { tagGroups: [{ tags: ["user:alice"], match: "all_strict" }] },
    });

    expect(sentTrigger(stub).tag_groups).toEqual([
      { tags: ["user:alice"], match: "all_strict" },
    ]);
  });

  it("still maps refreshAfterConsolidation", async () => {
    const { client: api, stub } = client();

    await api.createMentalModel("bank", "Auto", "q", {
      trigger: { refreshAfterConsolidation: true },
    });

    expect(sentTrigger(stub).refresh_after_consolidation).toBe(true);
  });

  it("maps an explicit 0 rather than dropping it as falsy", async () => {
    // 0 is a meaningful value here — it exempts one model from a bank-wide floor —
    // so the mapping must not treat it like an omitted field.
    const { client: api, stub } = client();

    await api.createMentalModel("bank", "Hot", "q", { trigger: { minRefreshIntervalSeconds: 0 } });

    expect(sentTrigger(stub).min_refresh_interval_seconds).toBe(0);
  });

  it("routes the create to the bank's mental-models collection", async () => {
    const { client: api, stub } = client();

    await api.createMentalModel("bank", "Plain", "q");

    expect(call(stub).method).toBe("POST");
    expect(call(stub).path).toBe("/v1/default/banks/bank/mental-models");
    expect(sentBody(stub).name).toBe("Plain");
    expect(sentBody(stub).source_query).toBe("q");
  });

  it("omitting trigger sends no trigger, preserving the server's default", async () => {
    const { client: api, stub } = client();

    await api.createMentalModel("bank", "Plain", "q");

    expect("trigger" in sentBody(stub)).toBe(false);
  });
});