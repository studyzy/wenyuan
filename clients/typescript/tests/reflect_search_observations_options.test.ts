/**
 * The search_observations budget options in the hand-written wrapper.
 *
 * These are per-call overrides of the bank's reflect_default_options; a wrapper
 * that dropped them would leave every TypeScript consumer stuck on the bank
 * default with nothing in the response to say the argument was ignored.
 */
import { describe, expect, it } from "vitest";

import { WenYuanClient } from "../src/index";
import { call, jsonBody, stubFetch } from "./helpers";

function client() {
  const stub = stubFetch(() => ({ json: { text: "" } }));
  return {
    stub,
    client: new WenYuanClient({ baseUrl: "http://api.test", fetch: stub.fetch }),
  };
}

describe("reflect search_observations options mapping", () => {
  it("omitting the options leaves them unset so the bank decides", async () => {
    const { client: api, stub } = client();

    await api.reflect("bank-1", "what did we decide");

    const body = jsonBody(call(stub));
    expect("reflect_search_observations_max_tokens" in body).toBe(false);
    expect("reflect_search_observations_include_entities" in body).toBe(false);
  });

  it("sends both options when they are set", async () => {
    const { client: api, stub } = client();

    await api.reflect("bank-1", "what did we decide", {
      reflectSearchObservationsMaxTokens: 3000,
      reflectSearchObservationsIncludeEntities: false,
    });

    const body = jsonBody(call(stub));
    expect(body.reflect_search_observations_max_tokens).toBe(3000);
    expect(body.reflect_search_observations_include_entities).toBe(false);
  });

  it("maps the reasoning filters and the tags filter", async () => {
    const { client: api, stub } = client();

    await api.reflect("bank-1", "why?", {
      factTypes: ["observation"],
      excludeMentalModels: true,
      excludeMentalModelIds: ["mm-9"],
      tags: ["project"],
      tagsMatch: "any_strict",
      context: "release review",
      responseSchema: { type: "object" },
    });

    const body = jsonBody(call(stub));
    expect(body.fact_types).toEqual(["observation"]);
    expect(body.exclude_mental_models).toBe(true);
    expect(body.exclude_mental_model_ids).toEqual(["mm-9"]);
    expect(body.tags).toEqual(["project"]);
    expect(body.tags_match).toBe("any_strict");
    expect(body.context).toBe("release review");
    expect(body.response_schema).toEqual({ type: "object" });
  });
});