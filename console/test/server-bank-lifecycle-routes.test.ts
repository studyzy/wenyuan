import { afterEach, describe, expect, it } from "vitest";
import * as clone from "../app/api/banks/[bankId]/clone/route";
import * as consolidate from "../app/api/banks/[bankId]/consolidate/route";
import * as recover from "../app/api/banks/[bankId]/consolidation-recover/route";
import * as strategyPreview from "../app/api/banks/[bankId]/consolidation-strategies/preview/route";
import * as bankExport from "../app/api/banks/[bankId]/export/route";
import * as bankImport from "../app/api/banks/[bankId]/import/route";
import * as llmHealth from "../app/api/banks/[bankId]/health/llm/route";
import { stubFetch, withEnv } from "./support/fixtures";
import { expectNoErrorKey, makeRequest, readJson } from "./support/harness";

let restore: (() => void) | undefined;
afterEach(() => {
  restore?.();
  restore = undefined;
  delete process.env.HINDSIGHT_CP_DATAPLANE_API_URL;
});

function ctx(bankId: string) {
  return { params: Promise.resolve({ bankId }) };
}

describe("bank lifecycle route method sets", () => {
  it("binds clone, consolidate, recover, strategy preview, health/llm and import as POST", () => {
    expect(typeof clone.POST).toBe("function");
    expect(typeof consolidate.POST).toBe("function");
    expect(typeof recover.POST).toBe("function");
    expect(typeof strategyPreview.POST).toBe("function");
    expect(typeof llmHealth.POST).toBe("function");
    expect(typeof bankImport.POST).toBe("function");
  });

  it("binds export as GET", () => {
    expect(typeof bankExport.GET).toBe("function");
  });

  it("binds no other method on these routes", () => {
    for (const mod of [clone, consolidate, recover, strategyPreview, llmHealth, bankImport]) {
      expect(Object.keys(mod).sort()).toEqual(["POST"]);
    }
    expect(Object.keys(bankExport).sort()).toEqual(["GET"]);
  });
});

describe("clone requires a string target-bank-id", () => {
  it("returns 400 when the body is missing", async () => {
    const response = await clone.POST(makeRequest("http://localhost/api/banks/acme/clone", { method: "POST", body: "" }), ctx("acme"));
    expect(response.status).toBe(400);
    expectNoErrorKey(await readJson(response));
  });

  it("returns 400 when target-bank-id is not a string", async () => {
    const response = await clone.POST(
      makeRequest("http://localhost/api/banks/acme/clone", { method: "POST", body: JSON.stringify({ "target-bank-id": 7 }) }),
      ctx("acme"),
    );
    expect(response.status).toBe(400);
    expectNoErrorKey(await readJson(response));
  });

  it("forwards the target and only the caller-set scope flags, returning 202 on acceptance", async () => {
    const stub = stubFetch([{ status: 202, body: { status: "accepted" } }]);
    restore = stub.restore;
    await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      const response = await clone.POST(
        makeRequest("http://localhost/api/banks/acme/clone", {
          method: "POST",
          body: JSON.stringify({ "target-bank-id": "copy", "copy-history": true }),
        }),
        ctx("acme"),
      );
      expect(response.status).toBe(202);
      expect(stub.calls[0]).toBe("http://upstream.local/v1/default/banks/acme/clone");
    });
  });
});

describe("lifecycle routes proxy at their catalogued paths", () => {
  it("consolidate posts to the bank path with the encoded id", async () => {
    const stub = stubFetch([{ status: 200, body: {} }]);
    restore = stub.restore;
    await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      await consolidate.POST(makeRequest("http://localhost/api/banks/a%2Fb/consolidate", { method: "POST", body: "{}" }), ctx("a/b"));
      expect(stub.calls[0]).toBe("http://upstream.local/v1/default/banks/a%2Fb/consolidate");
    });
  });

  it("export gets the bank export path", async () => {
    const stub = stubFetch([{ status: 200, body: {} }]);
    restore = stub.restore;
    await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      await bankExport.GET(makeRequest("http://localhost/api/banks/acme/export"), ctx("acme"));
      expect(stub.calls[0]).toBe("http://upstream.local/v1/default/banks/acme/export");
    });
  });

  it("import forwards the dry-run query flag", async () => {
    const stub = stubFetch([{ status: 200, body: {} }]);
    restore = stub.restore;
    await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      await bankImport.POST(
        makeRequest("http://localhost/api/banks/acme/import?dry_run=true", { method: "POST", body: "{}" }),
        ctx("acme"),
      );
      expect(stub.calls[0]).toContain("dry_run=true");
    });
  });
});