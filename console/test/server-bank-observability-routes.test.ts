import { afterEach, describe, expect, it } from "vitest";
import * as auditLogs from "../app/api/banks/[bankId]/audit-logs/route";
import * as auditStats from "../app/api/banks/[bankId]/audit-logs/stats/route";
import * as llmRequests from "../app/api/banks/[bankId]/llm-requests/route";
import * as llmStats from "../app/api/banks/[bankId]/llm-requests/stats/route";
import * as attachment from "../app/api/banks/[bankId]/attachments/[attachmentId]/route";
import * as operation from "../app/api/banks/[bankId]/operations/[operationId]/route";
import { stubFetch, withEnv } from "./support/fixtures";
import { expectNoErrorKey, makeRequest, readJson } from "./support/harness";

let restore: (() => void) | undefined;
afterEach(() => {
  restore?.();
  restore = undefined;
  delete process.env.HINDSIGHT_CP_DATAPLANE_API_URL;
});

const VALID_ID = "a".repeat(64);

describe("observability route method sets", () => {
  it("binds audit logs, llm requests and their stats as GET", () => {
    expect(Object.keys(auditLogs).sort()).toEqual(["GET"]);
    expect(Object.keys(auditStats).sort()).toEqual(["GET"]);
    expect(Object.keys(llmRequests).sort()).toEqual(["GET"]);
    expect(Object.keys(llmStats).sort()).toEqual(["GET"]);
  });

  it("binds attachments as GET and operations as GET/POST/DELETE", () => {
    expect(Object.keys(attachment).sort()).toEqual(["GET"]);
    expect(Object.keys(operation).sort()).toEqual(["DELETE", "GET", "POST"]);
  });
});

describe("observability query passthrough", () => {
  it("passes audit-log query parameters upstream", async () => {
    const stub = stubFetch([{ status: 200, body: {} }]);
    restore = stub.restore;
    await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      await auditLogs.GET(makeRequest("http://localhost/api/banks/acme/audit-logs?limit=5"), {
        params: Promise.resolve({ bankId: "acme" }),
      });
      expect(stub.calls[0]).toContain("/v1/default/banks/acme/audit-logs");
      expect(stub.calls[0]).toContain("limit=5");
    });
  });

  it("passes llm-request stats query parameters upstream", async () => {
    const stub = stubFetch([{ status: 200, body: {} }]);
    restore = stub.restore;
    await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      await llmStats.GET(makeRequest("http://localhost/api/banks/acme/llm-requests/stats?period=7d"), {
        params: Promise.resolve({ bankId: "acme" }),
      });
      expect(stub.calls[0]).toContain("/v1/default/banks/acme/llm-requests/stats");
      expect(stub.calls[0]).toContain("period=7d");
    });
  });
});

describe("observability success statuses follow the contract", () => {
  it("returns 200 for audit logs, audit stats, llm requests and llm stats", async () => {
    for (const [label, call] of [
      [
        "audit logs",
        () =>
          auditLogs.GET(makeRequest("http://localhost/api/banks/acme/audit-logs"), {
            params: Promise.resolve({ bankId: "acme" }),
          }),
      ],
      [
        "audit stats",
        () =>
          auditStats.GET(makeRequest("http://localhost/api/banks/acme/audit-logs/stats"), {
            params: Promise.resolve({ bankId: "acme" }),
          }),
      ],
      [
        "llm requests",
        () =>
          llmRequests.GET(makeRequest("http://localhost/api/banks/acme/llm-requests"), {
            params: Promise.resolve({ bankId: "acme" }),
          }),
      ],
      [
        "llm stats",
        () =>
          llmStats.GET(makeRequest("http://localhost/api/banks/acme/llm-requests/stats"), {
            params: Promise.resolve({ bankId: "acme" }),
          }),
      ],
    ] as const) {
      const stub = stubFetch([{ status: 201, body: { ok: true } }]);
      restore = stub.restore;
      await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
        const response = await call();
        expect(response.status, `${label} should report 200`).toBe(200);
      });
    }
  });
});

describe("observability failures use the localized envelope", () => {
  it("maps an upstream failure to its status without leaking the internal key", async () => {
    for (const [label, call] of [
      [
        "audit logs",
        () =>
          auditLogs.GET(
            makeRequest("http://localhost/api/banks/acme/audit-logs", { acceptLanguage: "en" }),
            { params: Promise.resolve({ bankId: "acme" }) },
          ),
      ],
      [
        "llm stats",
        () =>
          llmStats.GET(
            makeRequest("http://localhost/api/banks/acme/llm-requests/stats", {
              acceptLanguage: "en",
            }),
            { params: Promise.resolve({ bankId: "acme" }) },
          ),
      ],
    ] as const) {
      const stub = stubFetch([{ status: 429, body: { detail: "rate limited" } }]);
      restore = stub.restore;
      await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
        const response = await call();
        expect(response.status, `${label} passes the upstream status`).toBe(429);
        const payload = await readJson(response);
        expect(typeof payload.error).toBe("string");
        expect((payload.error as string).length).toBeGreaterThan(0);
        expectNoErrorKey(payload);
        expect(payload.upstream).toMatchObject({ status: 429 });
      });
    }
  });

  it("reports 502 when the upstream is unreachable", async () => {
    const stub = stubFetch([{ reject: true }]);
    restore = stub.restore;
    await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      const response = await llmRequests.GET(
        makeRequest("http://localhost/api/banks/acme/llm-requests", { acceptLanguage: "en" }),
        { params: Promise.resolve({ bankId: "acme" }) },
      );
      expect(response.status).toBe(502);
      const payload = await readJson(response);
      expect(typeof payload.error).toBe("string");
      expectNoErrorKey(payload);
    });
  });
});

describe("attachment proxy guard", () => {
  it("streams upstream bytes with the upstream content type and a no-sniff header", async () => {
    const stub = stubFetch([{ status: 200, body: "binary", contentType: "image/png" }]);
    restore = stub.restore;
    await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      const response = await attachment.GET(
        makeRequest(`http://localhost/api/banks/acme/attachments/${VALID_ID}`),
        { params: Promise.resolve({ bankId: "acme", attachmentId: VALID_ID }) },
      );
      expect(response.status).toBe(200);
      expect(response.headers.get("content-type")).toBe("image/png");
      expect(response.headers.get("x-content-type-options")).toBe("nosniff");
      expect(stub.calls[0]).toBe(
        `http://upstream.local/v1/default/banks/acme/attachments/${VALID_ID}`,
      );
    });
  });

  it("rejects a malformed id with 400 without fetching upstream", async () => {
    const stub = stubFetch([{ status: 200, body: {} }]);
    restore = stub.restore;
    const response = await attachment.GET(
      makeRequest("http://localhost/api/banks/acme/attachments/not-a-hex-id"),
      { params: Promise.resolve({ bankId: "acme", attachmentId: "not-a-hex-id" }) },
    );
    expect(response.status).toBe(400);
    expectNoErrorKey(await readJson(response));
    expect(stub.calls).toHaveLength(0);
  });

  it("rejects an uppercase id", async () => {
    const response = await attachment.GET(
      makeRequest("http://localhost/api/banks/acme/attachments/AAA"),
      { params: Promise.resolve({ bankId: "acme", attachmentId: "A".repeat(64) }) },
    );
    expect(response.status).toBe(400);
  });
});

describe("per-operation route", () => {
  it("reads, retries and deletes at the catalogued path", async () => {
    const stub = stubFetch([{ status: 200, body: {} }, { status: 200, body: {} }, { status: 200, body: { deleted: true } }]);
    restore = stub.restore;
    await withEnv({ HINDSIGHT_CP_DATAPLANE_API_URL: "http://upstream.local" }, async () => {
      const context = { params: Promise.resolve({ bankId: "acme", operationId: "op:1" }) };
      await operation.GET(makeRequest("http://localhost/api/banks/acme/operations/op%3A1"), context);
      await operation.POST(makeRequest("http://localhost/api/banks/acme/operations/op%3A1", { method: "POST", body: "{}" }), context);
      await operation.DELETE(makeRequest("http://localhost/api/banks/acme/operations/op%3A1", { method: "DELETE" }), context);
      expect(stub.calls.every((call) => call.includes("/v1/default/banks/acme/operations/op%3A1"))).toBe(true);
    });
  });
});