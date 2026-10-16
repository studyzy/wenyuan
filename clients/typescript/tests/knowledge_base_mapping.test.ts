/**
 * The knowledge-base wrappers' request mapping.
 *
 * These need no server: what the generated client would send is recorded, so the
 * camelCase options can be checked against the snake_case request — and, for
 * updateKnowledgeNode, that an omitted field is genuinely absent rather than sent
 * as undefined, because the server distinguishes "not provided" from
 * `parent_id: null`, which means "move to the root".
 */
import { describe, expect, it } from "vitest";

import { WenYuanClient } from "../src/index";
import { call, jsonBody, jsonObject, stubFetch, type Stub } from "./helpers";

const TAG_GROUPS = [
  { or: [{ tags: ["user:kate"], match: "all_strict" as const }, { tags: ["team"] }] },
];

function client() {
  const stub = stubFetch(() => ({ json: { roots: [], page_id: "kp-1", id: "kp-1" } }));
  return {
    stub,
    client: new WenYuanClient({ baseUrl: "http://api.test", fetch: stub.fetch }),
  };
}

function sentBody(stub: Stub): Record<string, unknown> {
  return jsonBody(call(stub));
}

describe("createKnowledgePage mapping", () => {
  it("maps name, source query, parent and tags", async () => {
    const { client: api, stub } = client();

    await api.createKnowledgePage("bank", "Deploying the API", "How is the API deployed?", {
      parentId: "kf-1",
      tags: ["ops", "type:runbook"],
      maxTokens: 8192,
    });

    const body = sentBody(stub);
    expect(call(stub).path).toBe("/v1/default/banks/bank/knowledge-base/pages");
    expect(body.name).toBe("Deploying the API");
    expect(body.source_query).toBe("How is the API deployed?");
    expect(body.parent_id).toBe("kf-1");
    expect(body.tags).toEqual(["ops", "type:runbook"]);
    expect(body.max_tokens).toBe(8192);
  });

  it("omitting trigger sends no trigger, preserving the page defaults", async () => {
    const { client: api, stub } = client();

    await api.createKnowledgePage("bank", "Page", "q");

    expect("trigger" in sentBody(stub)).toBe(false);
  });

  it("threads every trigger field through, snake_cased", async () => {
    const { client: api, stub } = client();

    await api.createKnowledgePage("bank", "Page", "q", {
      trigger: {
        mode: "delta",
        refreshAfterConsolidation: true,
        factTypes: ["observation"],
        excludeMentalModels: true,
        excludeMentalModelIds: ["mm-9"],
        tagsMatch: "any",
        includeChunks: false,
        recallMaxTokens: 4096,
        recallChunksMaxTokens: 1024,
      },
    });

    const trigger = jsonObject(sentBody(stub).trigger, "trigger");
    expect(trigger.mode).toBe("delta");
    expect(trigger.refresh_after_consolidation).toBe(true);
    expect(trigger.fact_types).toEqual(["observation"]);
    expect(trigger.exclude_mental_models).toBe(true);
    expect(trigger.exclude_mental_model_ids).toEqual(["mm-9"]);
    expect(trigger.tags_match).toBe("any");
    expect(trigger.include_chunks).toBe(false);
    expect(trigger.recall_max_tokens).toBe(4096);
    expect(trigger.recall_chunks_max_tokens).toBe(1024);
  });
});

describe("updateKnowledgeNode mapping", () => {
  it("sends only the fields provided", async () => {
    const { client: api, stub } = client();

    await api.updateKnowledgeNode("bank", "kp-1", { name: "Renamed" });

    const body = sentBody(stub);
    expect(body).toEqual({ name: "Renamed" });
    expect("parent_id" in body).toBe(false);
    expect(call(stub).method).toBe("PATCH");
  });

  it("sends an explicit null parentId, which moves the node to the root", async () => {
    const { client: api, stub } = client();

    await api.updateKnowledgeNode("bank", "kp-1", { parentId: null });

    const body = sentBody(stub);
    expect("parent_id" in body).toBe(true);
    expect(body.parent_id).toBeNull();
  });

  it("maps the page options to snake_case", async () => {
    const { client: api, stub } = client();

    await api.updateKnowledgeNode("bank", "kp-1", {
      sourceQuery: "New question?",
      tags: [],
      maxTokens: 2048,
    });

    const body = sentBody(stub);
    expect(body.source_query).toBe("New question?");
    expect(body.tags).toEqual([]);
    expect(body.max_tokens).toBe(2048);
  });

  it("passes a partial trigger through untouched", async () => {
    // The trigger is a PATCH server-side: what is not sent keeps the node's
    // current value, so it travels in the API's own shape.
    const { client: api, stub } = client();

    await api.updateKnowledgeNode("bank", "kp-1", { trigger: { refresh_cron: "0 3 * * *" } });

    expect(sentBody(stub).trigger).toEqual({ refresh_cron: "0 3 * * *" });
  });

  it("omits the trigger entirely when unset", async () => {
    const { client: api, stub } = client();

    await api.updateKnowledgeNode("bank", "kp-1", { maxTokens: 2048 });

    expect("trigger" in sentBody(stub)).toBe(false);
  });
});

describe("searchKnowledgeBase mapping", () => {
  it("sends the query as q and omits limit when unset", async () => {
    const { client: api, stub } = client();

    await api.searchKnowledgeBase("bank", "how do we deploy");

    expect(call(stub).path.startsWith("/v1/default/banks/bank/knowledge-base/search?")).toBe(true);
    expect(call(stub).query.get("q")).toBe("how do we deploy");
    expect(call(stub).query.get("limit")).toBeNull();
  });

  it("passes limit through when set", async () => {
    const { client: api, stub } = client();

    await api.searchKnowledgeBase("bank", "deploy", { limit: 5 });

    expect(call(stub).query.get("limit")).toBe("5");
  });
});

describe("knowledge-base tag filter mapping", () => {
  it("tree and search forward tags, tags_match and a JSON-encoded tag_groups", async () => {
    const { client: api, stub } = client();
    const filter = { tags: ["user:kate"], tagsMatch: "any_strict" as const, tagGroups: TAG_GROUPS };

    await api.getKnowledgeBaseTree("bank", filter);
    await api.searchKnowledgeBase("bank", "q", filter);

    for (const request of [call(stub, 0), call(stub, 1)]) {
      expect(request.query.getAll("tags")).toEqual(["user:kate"]);
      expect(request.query.get("tags_match")).toBe("any_strict");
      expect(JSON.parse(request.query.get("tag_groups") ?? "")).toEqual(TAG_GROUPS);
    }
  });

  it("omits the filter when it is unset", async () => {
    const { client: api, stub } = client();

    await api.getKnowledgeBaseTree("bank");

    expect(call(stub).path).toBe("/v1/default/banks/bank/knowledge-base/tree");
    expect(call(stub).query.toString()).toBe("");
  });
});

describe("knowledge-base write routes", () => {
  it("creates a folder under the parent", async () => {
    const { client: api, stub } = client();

    await api.createKnowledgeFolder("bank", "Runbooks", { parentId: null });

    expect(call(stub).path).toBe("/v1/default/banks/bank/knowledge-base/folders");
    expect(sentBody(stub)).toEqual({ name: "Runbooks", parent_id: null });
  });

  it("reads a page and exports the knowledge base", async () => {
    const { client: api, stub } = client();

    await api.getKnowledgePage("bank", "kp-1");
    expect(call(stub, 0).path).toBe("/v1/default/banks/bank/knowledge-base/pages/kp-1");

    await api.exportKnowledgeBase("bank");
    expect(call(stub, 1).path).toBe("/v1/default/banks/bank/knowledge-base/export");
  });

  it("deletes a node", async () => {
    const { client: api, stub } = client();

    await api.deleteKnowledgeNode("bank", "kp-1");

    expect(call(stub).method).toBe("DELETE");
    expect(call(stub).path).toBe("/v1/default/banks/bank/knowledge-base/nodes/kp-1");
  });
});