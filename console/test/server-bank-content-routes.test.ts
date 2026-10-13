import { afterEach, describe, expect, it } from "vitest";
import * as directives from "../app/api/banks/[bankId]/directives/route";
import * as directive from "../app/api/banks/[bankId]/directives/[directiveId]/route";
import * as mentalModels from "../app/api/banks/[bankId]/mental-models/route";
import * as mentalModel from "../app/api/banks/[bankId]/mental-models/[mentalModelId]/route";
import * as clear from "../app/api/banks/[bankId]/mental-models/[mentalModelId]/clear/route";
import * as dryRunRefresh from "../app/api/banks/[bankId]/mental-models/[mentalModelId]/dry-run-refresh/route";
import * as history from "../app/api/banks/[bankId]/mental-models/[mentalModelId]/history/route";
import * as refresh from "../app/api/banks/[bankId]/mental-models/[mentalModelId]/refresh/route";
import * as observations from "../app/api/banks/[bankId]/observations/route";
import * as observation from "../app/api/banks/[bankId]/observations/[modelId]/route";
import * as scopes from "../app/api/banks/[bankId]/observations/scopes/route";
import * as promptPreview from "../app/api/banks/[bankId]/prompts/preview/route";
import * as dryRunExtract from "../app/api/banks/[bankId]/memories/dry-run-extract/route";
import * as webhooks from "../app/api/banks/[bankId]/webhooks/route";
import * as webhook from "../app/api/banks/[bankId]/webhooks/[webhookId]/route";
import * as deliveries from "../app/api/banks/[bankId]/webhooks/[webhookId]/deliveries/route";
import { stubFetch, withEnv } from "./support/fixtures";
import { expectNoErrorKey, makeRequest, readJson } from "./support/harness";

let restore: (() => void) | undefined;
afterEach(() => {
  restore?.();
  restore = undefined;
  delete process.env.HINDSIGHT_CP_DATAPLANE_API_URL;
});

const bank = (bankId: string) => ({ params: Promise.resolve({ bankId }) });
const model = (bankId: string, mentalModelId: string) =>
  ({ params: Promise.resolve({ bankId, mentalModelId }) });
const webhookCtx = (bankId: string, webhookId: string) =>
  ({ params: Promise.resolve({ bankId, webhookId }) });

describe("content route method sets", () => {
  it("binds directives and their detail", () => {
    expect(Object.keys(directives).sort()).toEqual(["GET", "POST"]);
    expect(Object.keys(directive).sort()).toEqual(["DELETE", "GET", "PATCH"]);
  });

  it("binds mental models and their children", () => {
    expect(Object.keys(mentalModels).sort()).toEqual(["GET", "POST"]);
    expect(Object.keys(mentalModel).sort()).toEqual(["DELETE", "GET", "PATCH"]);
    expect(Object.keys(clear).sort()).toEqual(["POST"]);
    expect(Object.keys(dryRunRefresh).sort()).toEqual(["POST"]);
    expect(Object.keys(refresh).sort()).toEqual(["POST"]);
    expect(Object.keys(history).sort()).toEqual(["GET"]);
  });

  it("binds observations children", () => {
    expect(Object.keys(observations).sort()).toEqual(["DELETE", "GET"]);
    expect(Object.keys(observation).sort()).toEqual(["GET"]);
    expect(Object.keys(scopes).sort()).toEqual(["GET"]);
  });

  it("binds prompts, extraction and webhooks", () => {
    expect(Object.keys(promptPreview).sort()).toEqual(["POST"]);
    expect(Object.keys(dryRunExtract).sort()).toEqual(["POST"]);
    expect(Object.keys(webhooks).sort()).toEqual(["GET", "POST"]);
    expect(Object.keys(webhook).sort()).toEqual(["DELETE", "PATCH"]);
    expect(Object.keys(deliveries).sort()).toEqual(["GET"]);
  });
});

describe("delete returns a small success payload", () => {
  it("directive delete returns a success object rather than the upstream body", async () => {
    const stub = stubFetch([{ status: 200, body: { deleted: "upstream-body" } }]);
    restore = stub.restore;
    await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      const response = await directive.DELETE(
        makeRequest("http://localhost/api/banks/acme/directives/d1", { method: "DELETE" }),
        { params: Promise.resolve({ bankId: "acme", directiveId: "d1" }) },
      );
      expect(response.status).toBe(200);
      expect(await readJson(response)).toEqual({ success: true });
    });
  });

  it("mental-model delete returns a success object", async () => {
    const stub = stubFetch([{ status: 200, body: { deleted: "upstream-body" } }]);
    restore = stub.restore;
    await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      const response = await mentalModel.DELETE(
        makeRequest("http://localhost/api/banks/acme/mental-models/m1", { method: "DELETE" }),
        model("acme", "m1"),
      );
      expect(await readJson(response)).toEqual({ success: true });
    });
  });
});

describe("child-resource 404 translation", () => {
  it("maps an upstream 404 on mental-model history to a localized 404", async () => {
    const stub = stubFetch([{ status: 404, body: { detail: "missing" } }]);
    restore = stub.restore;
    await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      const response = await history.GET(
        makeRequest("http://localhost/api/banks/acme/mental-models/m1/history"),
        model("acme", "m1"),
      );
      expect(response.status).toBe(404);
      const body = await readJson(response);
      expectNoErrorKey(body);
      expect(body.detail).not.toBe("missing");
    });
  });

  it("maps another upstream failure on mental-model history to a localized 500", async () => {
    const stub = stubFetch([{ status: 502, body: { detail: "bad gateway" } }]);
    restore = stub.restore;
    await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      const response = await history.GET(
        makeRequest("http://localhost/api/banks/acme/mental-models/m1/history"),
        model("acme", "m1"),
      );
      expect(response.status).toBe(500);
      expectNoErrorKey(await readJson(response));
    });
  });
});

describe("content routes proxy at their catalogued paths", () => {
  it("webhook deliveries forwards its limit and cursor, ignoring other paging params", async () => {
    const stub = stubFetch([{ status: 200, body: {} }]);
    restore = stub.restore;
    await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      await deliveries.GET(
        makeRequest("http://localhost/api/banks/acme/webhooks/w1/deliveries?limit=10&cursor=c1&offset=20"),
        webhookCtx("acme", "w1"),
      );
      expect(stub.calls[0]).toContain("/v1/default/banks/acme/webhooks/w1/deliveries");
      expect(stub.calls[0]).toContain("limit=10");
      expect(stub.calls[0]).toContain("cursor=c1");
      expect(stub.calls[0]).not.toContain("offset=20");
    });
  });

  it("webhook deliveries substitutes a default limit when the caller omits it", async () => {
    const stub = stubFetch([{ status: 200, body: {} }]);
    restore = stub.restore;
    await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      await deliveries.GET(
        makeRequest("http://localhost/api/banks/acme/webhooks/w1/deliveries"),
        webhookCtx("acme", "w1"),
      );
      expect(stub.calls[0]).toContain("limit=50");
    });
  });

  it("observations/[modelId] reads at its catalogued path", async () => {
    const stub = stubFetch([{ status: 200, body: {} }]);
    restore = stub.restore;
    await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      await observation.GET(makeRequest("http://localhost/api/banks/acme/observations/m1"), {
        params: Promise.resolve({ bankId: "acme", modelId: "m1" }),
      });
      expect(stub.calls[0]).toBe("http://upstream.local/v1/default/banks/acme/observations/m1");
    });
  });

  it("observation scopes reads at its catalogued path", async () => {
    const stub = stubFetch([{ status: 200, body: {} }]);
    restore = stub.restore;
    await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      await scopes.GET(makeRequest("http://localhost/api/banks/acme/observations/scopes"), bank("acme"));
      expect(stub.calls[0]).toBe("http://upstream.local/v1/default/banks/acme/observations/scopes");
    });
  });

  it("prompt preview posts at its catalogued path", async () => {
    const stub = stubFetch([{ status: 200, body: {} }]);
    restore = stub.restore;
    await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      await promptPreview.POST(
        makeRequest("http://localhost/api/banks/acme/prompts/preview", { method: "POST", body: "{}" }),
        bank("acme"),
      );
      expect(stub.calls[0]).toBe("http://upstream.local/v1/default/banks/acme/prompts/preview");
    });
  });
});
describe("content success statuses follow the contract", () => {
  it("returns 201 for directive and webhook create", async () => {
    for (const [label, call] of [
      [
        "directive create",
        () =>
          directives.POST(makeRequest("http://localhost/api/banks/acme/directives", { method: "POST", body: "{}" }), {
            params: Promise.resolve({ bankId: "acme" }),
          }),
      ],
      [
        "webhook create",
        () =>
          webhooks.POST(makeRequest("http://localhost/api/banks/acme/webhooks", { method: "POST", body: "{}" }), {
            params: Promise.resolve({ bankId: "acme" }),
          }),
      ],
    ] as const) {
      const stub = stubFetch([{ status: 200, body: { ok: true } }]);
      restore = stub.restore;
      await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
        const response = await call();
        expect(response.status, `${label} should report 201`).toBe(201);
      });
    }
  });

  it("returns 202 for mental-model create", async () => {
    const stub = stubFetch([{ status: 200, body: { operation_id: "op-1" } }]);
    restore = stub.restore;
    await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      const response = await mentalModels.POST(
        makeRequest("http://localhost/api/banks/acme/mental-models", { method: "POST", body: "{}" }),
        { params: Promise.resolve({ bankId: "acme" }) },
      );
      expect(response.status).toBe(202);
    });
  });

  it("returns 200 for the list and read routes even when the upstream says 201", async () => {
    for (const [label, call] of [
      [
        "directive list",
        () =>
          directives.GET(makeRequest("http://localhost/api/banks/acme/directives"), {
            params: Promise.resolve({ bankId: "acme" }),
          }),
      ],
      [
        "mental-model list",
        () =>
          mentalModels.GET(makeRequest("http://localhost/api/banks/acme/mental-models"), {
            params: Promise.resolve({ bankId: "acme" }),
          }),
      ],
      [
        "observation scopes",
        () =>
          scopes.GET(makeRequest("http://localhost/api/banks/acme/observations/scopes"), {
            params: Promise.resolve({ bankId: "acme" }),
          }),
      ],
    ] as const) {
      const stub = stubFetch([{ status: 201, body: {} }]);
      restore = stub.restore;
      await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
        const response = await call();
        expect(response.status, `${label} should report 200`).toBe(200);
      });
    }
  });
});

describe("observations collection reshapes memories", () => {
  it("pins the observation type and limit, then returns an items object", async () => {
    const stub = stubFetch([
      {
        status: 200,
        body: {
          items: [{ id: "m1", text: "hello", tags: ["a"], date: "2026-01-01T00:00:00Z" }],
        },
      },
    ]);
    restore = stub.restore;
    await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      const response = await observations.GET(makeRequest("http://localhost/api/banks/acme/observations"), {
        params: Promise.resolve({ bankId: "acme" }),
      });
      expect(response.status).toBe(200);
      expect(stub.calls[0]).toContain("type=observation");
      expect(stub.calls[0]).toContain("limit=1000");

      const payload = await readJson(response);
      const items = payload.items as Array<Record<string, unknown>>;
      expect(Array.isArray(items)).toBe(true);
      expect(items[0]).toMatchObject({
        id: "m1",
        bank_id: "acme",
        text: "hello",
        proof_count: 1,
        tags: ["a"],
        source_memory_ids: [],
        source_memories: [],
        created_at: "2026-01-01T00:00:00Z",
      });
    });
  });

  it("maps an upstream failure to 500 rather than passing the status through", async () => {
    const stub = stubFetch([{ status: 422, body: { detail: "nope" } }]);
    restore = stub.restore;
    await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      const response = await observations.GET(
        makeRequest("http://localhost/api/banks/acme/observations", { acceptLanguage: "en" }),
        { params: Promise.resolve({ bankId: "acme" }) },
      );
      expect(response.status).toBe(500);
      const payload = await readJson(response);
      expectNoErrorKey(payload);
    });
  });
});

describe("tag filter forwarding is guarded", () => {
  it("forwards tags with a known mode and drops an unknown mode", async () => {
    for (const [query, expectMode] of [
      ["tags=a&tags=b&tags_match=all", true],
      ["tags=a&tags_match=bogus", false],
    ] as const) {
      const stub = stubFetch([{ status: 200, body: {} }]);
      restore = stub.restore;
      await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
        await directives.GET(makeRequest(`http://localhost/api/banks/acme/directives?${query}`), {
          params: Promise.resolve({ bankId: "acme" }),
        });
        const called = stub.calls[0] ?? "";
        expect(called).toContain("tags=a");
        expect(called.includes("tags_match="), `${query} -> tags_match forwarded?`).toBe(expectMode);
      });
    }
  });

  it("does not forward tags_match on its own", async () => {
    const stub = stubFetch([{ status: 200, body: {} }]);
    restore = stub.restore;
    await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      await directives.GET(makeRequest("http://localhost/api/banks/acme/directives?tags_match=all"), {
        params: Promise.resolve({ bankId: "acme" }),
      });
      expect(stub.calls[0]).not.toContain("tags_match");
    });
  });
});

describe("content failures use the localized envelope", () => {
  it("keeps the upstream status on a 4xx failure and strips the internal key", async () => {
    const stub = stubFetch([{ status: 403, body: { detail: "forbidden" } }]);
    restore = stub.restore;
    await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      const response = await directives.POST(
        makeRequest("http://localhost/api/banks/acme/directives", {
          method: "POST",
          acceptLanguage: "en",
          body: "{}",
        }),
        { params: Promise.resolve({ bankId: "acme" }) },
      );
      expect(response.status).toBe(403);
      const payload = await readJson(response);
      expect(typeof payload.error).toBe("string");
      expectNoErrorKey(payload);
      expect(payload.upstream).toMatchObject({ status: 403 });
    });
  });
});
