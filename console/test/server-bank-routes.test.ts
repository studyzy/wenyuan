import { afterEach, describe, expect, it } from "vitest";
import * as banksCollection from "../app/api/banks/route";
import * as bankDetail from "../app/api/banks/[bankId]/route";
import * as aliases from "../app/api/banks/[bankId]/aliases/route";
import * as config from "../app/api/banks/[bankId]/config/route";
import * as webhooks from "../app/api/banks/[bankId]/webhooks/route";
import * as mentalModels from "../app/api/banks/[bankId]/mental-models/route";
import * as observations from "../app/api/banks/[bankId]/observations/route";
import { stubFetch, withEnv } from "./support/fixtures";
import { expectNoErrorKey, makeRequest, readJson } from "./support/harness";

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
      const payload = await readJson(response);
      expect(typeof payload.error).toBe("string");
      expectNoErrorKey(payload);
      expect(payload.upstream).toMatchObject({ status: 404 });
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
describe("bank collection and detail follow the contract", () => {
  it("returns 201 on create and requires a bank id in the body", async () => {
    const stub = stubFetch([{ status: 200, body: { bank_id: "acme" } }]);
    restore = stub.restore;
    await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      const created = await banksCollection.POST(
        makeRequest("http://localhost/api/banks", {
          method: "POST",
          body: JSON.stringify({ bank_id: "acme" }),
        }),
      );
      expect(created.status).toBe(201);
    });

    const missing = await banksCollection.POST(
      makeRequest("http://localhost/api/banks", {
        method: "POST",
        body: JSON.stringify({ name: "no id" }),
      }),
    );
    expect(missing.status).toBe(400);
    expectNoErrorKey(await readJson(missing));
  });

  it("rejects an unparseable create body with 400", async () => {
    const missing = await banksCollection.POST(
      makeRequest("http://localhost/api/banks", { method: "POST", body: "{oops" }),
    );
    expect(missing.status).toBe(400);
    expectNoErrorKey(await readJson(missing));
  });

  it("returns 200 for bank detail update and delete", async () => {
    for (const call of [
      () =>
        bankDetail.PUT(makeRequest("http://localhost/api/banks/acme", { method: "PUT", body: "{}" }), {
          params: Promise.resolve({ bankId: "acme" }),
        }),
      () =>
        bankDetail.PATCH(makeRequest("http://localhost/api/banks/acme", { method: "PATCH", body: "{}" }), {
          params: Promise.resolve({ bankId: "acme" }),
        }),
      () =>
        bankDetail.DELETE(makeRequest("http://localhost/api/banks/acme", { method: "DELETE" }), {
          params: Promise.resolve({ bankId: "acme" }),
        }),
    ]) {
      const stub = stubFetch([{ status: 201, body: {} }]);
      restore = stub.restore;
      await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
        const response = await call();
        expect(response.status).toBe(200);
      });
    }
  });
});
