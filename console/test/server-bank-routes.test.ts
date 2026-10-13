import { afterEach, describe, expect, it } from "vitest";
import * as banksCollection from "../app/api/banks/route";
import * as bankDetail from "../app/api/banks/[bankId]/route";
import * as aliases from "../app/api/banks/[bankId]/aliases/route";
import * as config from "../app/api/banks/[bankId]/config/route";
import * as webhooks from "../app/api/banks/[bankId]/webhooks/route";
import * as mentalModels from "../app/api/banks/[bankId]/mental-models/route";
import * as observations from "../app/api/banks/[bankId]/observations/route";
import { stubFetch, withEnv } from "./support/fixtures";
import { makeRequest, readJson } from "./support/harness";

let restore: (() => void) | undefined;
afterEach(() => {
  restore?.();
  restore = undefined;
  delete process.env.HINDSIGHT_CP_DATAPLANE_API_URL;
  delete process.env.HINDSIGHT_CP_DATAPLANE_API_KEY;
});

describe("bank proxy encoding", () => {
  it("percent-encodes the bank id in the upstream URL", async () => {
    const stub = stubFetch([{ status: 200, body: { bank_id: "team:alpha" } }]);
    restore = stub.restore;
    await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      const response = await bankDetail.PATCH(
        makeRequest("http://localhost/api/banks/team%3Aalpha", { method: "PATCH", body: "{}" }),
        { params: Promise.resolve({ bankId: "team:alpha" }) },
      );
      expect(response.status).toBe(200);
      expect(stub.calls[0]).toContain("/v1/default/banks/team%3Aalpha");
    });
  });

  it("builds sub-resource paths with the encoded bank id", async () => {
    const stub = stubFetch([{ status: 200, body: {} }]);
    restore = stub.restore;
    await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      await config.GET(makeRequest("http://localhost/api/banks/a%2Fb/config"), {
        params: Promise.resolve({ bankId: "a/b" }),
      });
      expect(stub.calls[0]).toContain("/v1/default/banks/a%2Fb/config");
    });
  });
});

describe("upstream status passthrough", () => {
  it("returns the upstream status code on failure", async () => {
    const stub = stubFetch([{ status: 404, body: { error: "not found" } }]);
    restore = stub.restore;
    await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      const response = await bankDetail.PATCH(
        makeRequest("http://localhost/api/banks/acme", { method: "PATCH", body: "{}" }),
        { params: Promise.resolve({ bankId: "acme" }) },
      );
      expect(response.status).toBe(404);
      expect(await readJson(response)).toEqual({ error: "not found" });
    });
  });

  it("returns the upstream payload on success", async () => {
    const stub = stubFetch([{ status: 200, body: { bank_id: "acme", name: "Acme" } }]);
    restore = stub.restore;
    await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      const response = await banksCollection.GET(makeRequest("http://localhost/api/banks"));
      expect(response.status).toBe(200);
      expect(await readJson(response)).toEqual({ bank_id: "acme", name: "Acme" });
    });
  });
});

describe("route method sets", () => {
  it("binds the bank collection methods", () => {
    expect(typeof banksCollection.GET).toBe("function");
    expect(typeof banksCollection.POST).toBe("function");
  });

  it("binds bank lifecycle update/patch/delete", () => {
    expect(Object.keys(bankDetail).sort()).toEqual(["DELETE", "PATCH", "PUT"]);
  });

  it("binds config get/patch/delete", () => {
    expect(typeof config.GET).toBe("function");
    expect(typeof config.PATCH).toBe("function");
    expect(typeof config.DELETE).toBe("function");
  });

  it("binds aliases, webhooks, mental-models and observations", () => {
    expect(Object.keys(aliases).sort()).toEqual(["GET", "POST"]);
    expect(Object.keys(webhooks).sort()).toEqual(["GET", "POST"]);
    expect(Object.keys(mentalModels).sort()).toEqual(["GET", "POST"]);
    expect(Object.keys(observations).sort()).toEqual(["DELETE", "GET"]);
  });
});