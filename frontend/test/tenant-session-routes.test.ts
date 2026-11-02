import { describe, expect, it } from "vitest";
import { POST as enterTenant } from "../app/api/admin/tenants/[tenantId]/session/route";
import { DELETE as exitTenant, GET as sessionStatus } from "../app/api/admin/session/route";
import { createSessionToken } from "../lib/server/session";
import { OPERATOR_SUBJECT } from "../lib/server/subjects";
import { stubFetch, withEnv } from "./support/fixtures";
import { makeRequest, readJson } from "./support/harness";

const ENV = {
  WENYUAN_CP_ACCESS_KEY: "console-secret",
  WENYUAN_CP_ADMIN_API_URL: "http://admin.internal:18890",
  WENYUAN_CP_ADMIN_API_KEY: "operator-key",
};

const params = { params: Promise.resolve({ tenantId: "acme" }) };

function operatorCookie(): string {
  return "wenyuan_cp_access=" + createSessionToken("operator-key", OPERATOR_SUBJECT, Date.now());
}

async function withSessionStub(
  responses: Array<{ status?: number; body?: unknown }>,
  run: (stub: ReturnType<typeof stubFetch>) => Promise<void>,
): Promise<void> {
  await withEnv(ENV, async () => {
    const stub = stubFetch(responses);
    try {
      await run(stub);
    } finally {
      stub.restore();
    }
  });
}

function enter(cookie: string | undefined) {
  return enterTenant(
    makeRequest("http://localhost/api/admin/tenants/acme/session", {
      method: "POST",
      body: "{}",
      cookie,
    }),
    params,
  );
}

describe("tenant session BFF", () => {
  it("enters a tenant without minting a key", async () => {
    await withSessionStub(
      [{ status: 200, body: { tenant_id: "acme", status: "active" } }],
      async (stub) => {
        const response = await enter(operatorCookie());
        expect(response.status).toBe(201);
        const setCookie = response.headers.get("set-cookie") ?? "";
        expect(setCookie).toContain("wenyuan_cp_tenant=");
        expect(setCookie).toContain("HttpOnly");
        expect(setCookie).toContain("Max-Age=600");

        const headers = new Headers(stub.headers[0] as HeadersInit);
        expect(headers.get("authorization")).toBe("Bearer operator-key");
        expect(stub.calls).toEqual(["http://admin.internal:18890/admin/v1/tenants/acme"]);
        expect(stub.methods).toEqual(["GET"]);
      },
    );
  });

  it("reports the entered tenant without revealing a credential, and clears on exit", async () => {
    await withSessionStub(
      [{ status: 200, body: { tenant_id: "acme", status: "active" } }],
      async () => {
        const entered = await enter(operatorCookie());
        const cookie = (entered.headers.get("set-cookie") ?? "").split(";")[0];

        const status = await sessionStatus(
          makeRequest("http://localhost/api/admin/session", { cookie }),
        );
        expect(await readJson(status)).toEqual({ tenant_id: "acme" });

        const cleared = await exitTenant(
          makeRequest("http://localhost/api/admin/session", { method: "DELETE" }),
        );
        expect(cleared.headers.get("set-cookie")).toContain("Max-Age=0");
      },
    );
  });

  it("refuses to enter a tenant without an operator session", async () => {
    await withSessionStub([], async (stub) => {
      const response = await enter(
        "wenyuan_cp_access=" + createSessionToken("operator-key", "tenant:acme", Date.now()),
      );
      expect(response.status).toBe(403);
      expect(await readJson(response)).toEqual({ error: "operator_required" });
      expect(stub.calls).toHaveLength(0);
    });
  });

  it("refuses a tenant the data plane would not serve", async () => {
    await withSessionStub(
      [{ status: 200, body: { tenant_id: "acme", status: "suspended" } }],
      async (stub) => {
        const response = await enter(operatorCookie());
        expect(response.status).toBe(409);
        expect(await readJson(response)).toEqual({
          error: "tenant_not_active",
          status: "suspended",
        });
        expect(stub.calls).toHaveLength(1);
      },
    );
  });

  it("rejects a tampered cookie", async () => {
    await withSessionStub([], async () => {
      const response = await sessionStatus(
        makeRequest("http://localhost/api/admin/session", {
          cookie: "wenyuan_cp_tenant=not-a-token",
        }),
      );
      expect(await readJson(response)).toEqual({ tenant_id: null });
    });
  });

  it("passes an upstream failure through", async () => {
    await withSessionStub([{ status: 404, body: { error: "tenant not found" } }], async () => {
      const response = await enter(operatorCookie());
      expect(response.status).toBe(404);
    });
  });
});