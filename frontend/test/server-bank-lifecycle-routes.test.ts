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
  delete process.env.WENYUAN_CP_DATAPLANE_API_URL;
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

  it("forwards the target and only the caller-set scope flags on the query string, returning 202 on acceptance", async () => {
    const stub = stubFetch([{ status: 202, body: { status: "accepted" } }]);
    restore = stub.restore;
    await withEnv({ WENYUAN_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      const response = await clone.POST(
        makeRequest("http://localhost/api/banks/acme/clone", {
          method: "POST",
          body: JSON.stringify({ target_bank_id: "copy", include_history: true }),
        }),
        ctx("acme"),
      );
      expect(response.status).toBe(202);
      expect(stub.calls[0]).toBe(
        "http://upstream.local/v1/default/banks/acme/clone?target_bank_id=copy&include_history=true",
      );
    });
  });
});

describe("lifecycle routes proxy at their catalogued paths", () => {
  it("consolidate posts to the bank path with the encoded id", async () => {
    const stub = stubFetch([{ status: 200, body: {} }]);
    restore = stub.restore;
    await withEnv({ WENYUAN_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      await consolidate.POST(makeRequest("http://localhost/api/banks/a%2Fb/consolidate", { method: "POST", body: "{}" }), ctx("a/b"));
      expect(stub.calls[0]).toBe("http://upstream.local/v1/default/banks/a%2Fb/consolidate");
    });
  });

  it("export gets the bank export path", async () => {
    const stub = stubFetch([{ status: 200, body: {} }]);
    restore = stub.restore;
    await withEnv({ WENYUAN_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      await bankExport.GET(makeRequest("http://localhost/api/banks/acme/export"), ctx("acme"));
      expect(stub.calls[0]).toBe("http://upstream.local/v1/default/banks/acme/export");
    });
  });

  it("import forwards the dry-run query flag", async () => {
    const stub = stubFetch([{ status: 200, body: {} }]);
    restore = stub.restore;
    await withEnv({ WENYUAN_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      await bankImport.POST(
        makeRequest("http://localhost/api/banks/acme/import?dry_run=true", { method: "POST", body: "{}" }),
        ctx("acme"),
      );
      expect(stub.calls[0]).toContain("dry_run=true");
    });
  });
});

describe("lifecycle success statuses follow the contract", () => {
  it("returns 200 for export, import, consolidate, recover and strategy preview", async () => {
    for (const [label, call] of [
      ["export", () => bankExport.GET(makeRequest("http://localhost/api/banks/acme/export"), ctx("acme"))],
      [
        "import",
        () =>
          bankImport.POST(
            makeRequest("http://localhost/api/banks/acme/import", { method: "POST", body: "{}" }),
            ctx("acme"),
          ),
      ],
      [
        "consolidate",
        () =>
          consolidate.POST(
            makeRequest("http://localhost/api/banks/acme/consolidate", { method: "POST", body: "{}" }),
            ctx("acme"),
          ),
      ],
      [
        "recover",
        () =>
          recover.POST(
            makeRequest("http://localhost/api/banks/acme/consolidation-recover", {
              method: "POST",
              body: "{}",
            }),
            ctx("acme"),
          ),
      ],
      [
        "strategy preview",
        () =>
          strategyPreview.POST(
            makeRequest("http://localhost/api/banks/acme/consolidation-strategies/preview", {
              method: "POST",
              body: "{}",
            }),
            ctx("acme"),
          ),
      ],
    ] as const) {
      const stub = stubFetch([{ status: 201, body: { ok: true } }]);
      restore = stub.restore;
      await withEnv({ WENYUAN_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
        const response = await call();
        expect(response.status, `${label} should report its own success status`).toBe(200);
      });
    }
  });
});

describe("boolean query flags are strict", () => {
  it("sets dry_run only when the raw value is exactly the string true", async () => {
    for (const [query, expected] of [
      ["dry_run=true", true],
      ["dry_run=TRUE", false],
      ["dry_run=1", false],
      ["dry_run=false", false],
      ["", false],
    ] as const) {
      const stub = stubFetch([{ status: 200, body: {} }]);
      restore = stub.restore;
      await withEnv({ WENYUAN_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
        const url = `http://localhost/api/banks/acme/import${query ? `?${query}` : ""}`;
        await bankImport.POST(makeRequest(url, { method: "POST", body: "{}" }), ctx("acme"));
        const called = stub.calls[0] ?? "";
        expect(called.includes("dry_run=true"), `${query || "(none)"} -> dry_run set?`).toBe(expected);
      });
    }
  });

  it("sets export section flags only when each raw value is exactly true", async () => {
    const stub = stubFetch([{ status: 200, body: {} }]);
    restore = stub.restore;
    await withEnv({ WENYUAN_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      await bankExport.GET(
        makeRequest(
          "http://localhost/api/banks/acme/export?include_observations=true&exclude_parents=yes&include_knowledge_base=true",
        ),
        ctx("acme"),
      );
      const called = stub.calls[0] ?? "";
      expect(called).toContain("include_observations=true");
      expect(called).toContain("include_knowledge_base=true");
      expect(called).not.toContain("exclude_parents");
    });
  });
});

describe("lifecycle failures use the localized envelope", () => {
  it("maps an upstream failure to its status with the localized error and no internal key", async () => {
    const stub = stubFetch([{ status: 503, body: { detail: "upstream down" } }]);
    restore = stub.restore;
    await withEnv({ WENYUAN_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      const response = await bankExport.GET(
        makeRequest("http://localhost/api/banks/acme/export", { acceptLanguage: "en" }),
        ctx("acme"),
      );
      expect(response.status).toBe(503);
      const payload = await readJson(response);
      expect(typeof payload.error).toBe("string");
      expect((payload.error as string).length).toBeGreaterThan(0);
      expectNoErrorKey(payload);
      expect(payload.upstream).toMatchObject({ status: 503 });
    });
  });

  it("localizes a consolidate failure into the requested locale", async () => {
    const stub = stubFetch([{ status: 500, body: {} }]);
    restore = stub.restore;
    await withEnv({ WENYUAN_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      const response = await consolidate.POST(
        makeRequest("http://localhost/api/banks/acme/consolidate", {
          method: "POST",
          acceptLanguage: "zh-CN",
          body: "{}",
        }),
        ctx("acme"),
      );
      const payload = await readJson(response);
      expect(typeof payload.error).toBe("string");
      expectNoErrorKey(payload);
    });
  });
});