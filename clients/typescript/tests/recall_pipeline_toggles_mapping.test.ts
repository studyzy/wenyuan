/**
 * The recall pipeline toggles' request mapping.
 *
 * Both wrapper methods enumerate config fields rather than passing a dict
 * through, so a toggle added to the API but not to the wrapper is silently
 * dropped for every consumer of the SDK, with no type error to catch it.
 */
import { describe, expect, it } from "vitest";

import { WenYuanClient } from "../src/index";
import { call, jsonBody, jsonObject, stubFetch, type Stub } from "./helpers";

function client() {
  const stub = stubFetch(() => ({ json: { bank_id: "bank", name: "bank", config: {} } }));
  return {
    stub,
    client: new WenYuanClient({ baseUrl: "http://api.test", fetch: stub.fetch }),
  };
}

function sentUpdates(stub: Stub): Record<string, unknown> {
  return jsonObject(jsonBody(call(stub)).updates, "updates");
}

describe("recall pipeline toggle mapping", () => {
  it("createBank maps every toggle onto the body", async () => {
    const { client: api, stub } = client();

    await api.createBank("bank", {
      enableTextSearch: false,
      enableTemporalRetrieval: false,
      enableGraphRetrieval: false,
      enableReranking: false,
    });

    const body = jsonBody(call(stub));
    expect(body.enable_text_search).toBe(false);
    expect(body.enable_temporal_retrieval).toBe(false);
    expect(body.enable_graph_retrieval).toBe(false);
    expect(body.enable_reranking).toBe(false);
  });

  it("createBank omits unset toggles so the bank inherits the server default", async () => {
    const { client: api, stub } = client();

    await api.createBank("bank");

    const body = jsonBody(call(stub));
    expect("enable_text_search" in body).toBe(false);
    expect("enable_temporal_retrieval" in body).toBe(false);
    expect("enable_graph_retrieval" in body).toBe(false);
    expect("enable_reranking" in body).toBe(false);
  });

  it("updateBankConfig maps every toggle onto the updates map", async () => {
    const { client: api, stub } = client();

    await api.updateBankConfig("bank", {
      enableTextSearch: false,
      enableTemporalRetrieval: false,
      enableGraphRetrieval: false,
      enableReranking: false,
    });

    expect(sentUpdates(stub)).toEqual({
      enable_text_search: false,
      enable_temporal_retrieval: false,
      enable_graph_retrieval: false,
      enable_reranking: false,
    });
  });

  it("updateBankConfig sends only the toggles that were set", async () => {
    const { client: api, stub } = client();

    await api.updateBankConfig("bank", { enableTextSearch: false });

    expect(sentUpdates(stub)).toEqual({ enable_text_search: false });
  });
});