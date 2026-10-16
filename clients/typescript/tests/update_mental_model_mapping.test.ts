/**
 * The updateMentalModel wrapper's trigger mapping.
 *
 * The point of this suite is that a partial trigger stays partial. The server
 * patches a supplied trigger over the model's stored one and reads "named" from
 * the fields the request actually carried, so any field the caller did not set
 * must be absent from the body rather than filled in with a default — a mapper
 * that supplied its own defaults would silently reset the settings the caller
 * never mentioned.
 */
import { describe, expect, it } from "vitest";

import { WenYuanClient } from "../src/index";
import { call, jsonBody, jsonObject, stubFetch, type Stub } from "./helpers";

function client() {
  const stub = stubFetch(() => ({
    json: { id: "mm-1", bank_id: "bank", name: "Model", tags: [] },
  }));
  return {
    stub,
    client: new WenYuanClient({ baseUrl: "http://api.test", fetch: stub.fetch }),
  };
}

function sentTrigger(stub: Stub): Record<string, unknown> {
  return jsonObject(jsonBody(call(stub)).trigger, "trigger");
}

/** The keys a JSON body actually carries — `undefined` values are dropped in transit. */
function sentTriggerKeys(stub: Stub): string[] {
  return Object.entries(sentTrigger(stub))
    .map(([key]) => key)
    .sort();
}

describe("updateMentalModel trigger mapping", () => {
  it("sends only the setting the caller named", async () => {
    const { client: api, stub } = client();

    await api.updateMentalModel("bank", "mm-1", { trigger: { mode: "delta" } });

    expect(sentTrigger(stub).mode).toBe("delta");
    expect(sentTriggerKeys(stub)).toEqual(["mode"]);
  });

  it("keeps an explicit false rather than treating it as an omission", async () => {
    const { client: api, stub } = client();

    await api.updateMentalModel("bank", "mm-1", { trigger: { refreshAfterConsolidation: false } });

    expect(sentTriggerKeys(stub)).toEqual(["refresh_after_consolidation"]);
    expect(sentTrigger(stub).refresh_after_consolidation).toBe(false);
  });

  it("maps an explicit 0 rather than dropping it as falsy", async () => {
    const { client: api, stub } = client();

    await api.updateMentalModel("bank", "mm-1", { trigger: { minRefreshIntervalSeconds: 0 } });

    expect(sentTriggerKeys(stub)).toEqual(["min_refresh_interval_seconds"]);
    expect(sentTrigger(stub).min_refresh_interval_seconds).toBe(0);
  });

  it("sends an explicit null cron, which is how a schedule is removed", async () => {
    const { client: api, stub } = client();

    await api.updateMentalModel("bank", "mm-1", { trigger: { refreshCron: null } });

    expect(sentTriggerKeys(stub)).toEqual(["refresh_cron"]);
    expect(sentTrigger(stub).refresh_cron).toBeNull();
  });

  it("exposes every refresh setting the wrapper knows about", async () => {
    const { client: api, stub } = client();

    await api.updateMentalModel("bank", "mm-1", {
      trigger: {
        refreshCron: "0 3 * * *",
        tagsMatch: "any_strict",
        keepTrace: true,
        excludeMentalModels: true,
        factTypes: ["observation"],
        includeChunks: true,
        recallMaxTokens: 4096,
        recallChunksMaxTokens: 2048,
        reflectSearchObservationsMaxTokens: 3000,
        reflectSearchObservationsIncludeEntities: false,
        budget: "high",
        responseSchema: { type: "object" },
        excludeMentalModelIds: ["mm-other"],
        tagGroups: [{ tags: ["user:alice"], match: "all_strict" }],
      },
    });

    const trigger = sentTrigger(stub);
    expect(trigger.refresh_cron).toBe("0 3 * * *");
    expect(trigger.tags_match).toBe("any_strict");
    expect(trigger.keep_trace).toBe(true);
    expect(trigger.exclude_mental_models).toBe(true);
    expect(trigger.fact_types).toEqual(["observation"]);
    expect(trigger.include_chunks).toBe(true);
    expect(trigger.recall_max_tokens).toBe(4096);
    expect(trigger.recall_chunks_max_tokens).toBe(2048);
    expect(trigger.reflect_search_observations_max_tokens).toBe(3000);
    expect(trigger.reflect_search_observations_include_entities).toBe(false);
    expect(trigger.budget).toBe("high");
    expect(trigger.response_schema).toEqual({ type: "object" });
    expect(trigger.exclude_mental_model_ids).toEqual(["mm-other"]);
    expect(trigger.tag_groups).toEqual([{ tags: ["user:alice"], match: "all_strict" }]);
    // Still no mode: it was not asked for, and the stored one must survive.
    expect(sentTriggerKeys(stub)).not.toContain("mode");
  });

  it("omitting trigger sends no trigger at all", async () => {
    const { client: api, stub } = client();

    await api.updateMentalModel("bank", "mm-1", { name: "Renamed" });

    expect("trigger" in jsonBody(call(stub))).toBe(false);
    expect(call(stub).path).toBe("/v1/default/banks/bank/mental-models/mm-1");
  });
});