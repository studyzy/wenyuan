import { describe, expect, it } from "vitest";
import { POST as enterTenant } from "../app/api/admin/tenants/[tenantId]/session/route";
import { DELETE as exitTenant, GET as sessionStatus } from "../app/api/admin/session/route";
import { stubFetch, withEnv } from "./support/fixtures";
import { makeRequest, readJson } from "./support/harness";

const ENV = {
  WENYUAN_CP_ACCESS_KEY: "console-secret",
  WENYUAN_CP_ADMIN_API_URL: "http://admin.internal:18890",
  WENYUAN_CP_ADMIN_API_KEY: "operator-key",
};

const params = { params: Promise.resolve({ tenantId: "acme" }) };

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

describe("tenant session BFF", () => {
  it("mints a session key, encrypts it into an HttpOnly cookie and never returns it", async () => {
    await withSessionStub([{ status: 201, body: { plaintext: "wyk_tenant_secret", key: { key_id: "k1" } } }], async (stub) => {
      const response = await enterTenant(
        makeRequest("http://localhost/api/admin/tenants/acme/session", {
          method: "POST",
          body: "{}",
        }),
        params,
      );
      expect(response.status).toBe(201);
      const setCookie = response.headers.get("set-cookie") ?? "";
      expect(setCookie).toContain("wenyuan_cp_tenant=");
      expect(setCookie).toContain("HttpOnly");
      expect(setCookie).toContain("Max-Age=600");
      const body = await response.text();
      expect(body).not.toContain("wyk_tenant_secret");

      const headers = new Headers(stub.headers[0] as HeadersInit);
      expect(headers.get("authorization")).toBe("Bearer operator-key");
      expect(stub.calls[0]).toBe("http://admin.internal:18890/admin/v1/tenants/acme/session");
    });
  });

  it("reports the entered tenant without revealing the key, and clears on exit", async () => {
    await withSessionStub([{ status: 201, body: { plaintext: "wyk_tenant_secret" } }], async () => {
      const entered = await enterTenant(
        makeRequest("http://localhost/api/admin/tenants/acme/session", {
          method: "POST",
          body: "{}",
        }),
        params,
      );
      const cookie = (entered.headers.get("set-cookie") ?? "").split(";")[0];

      const status = await sessionStatus(
        makeRequest("http://localhost/api/admin/session", { cookie }),
      );
      expect(await readJson(status)).toEqual({ tenant_id: "acme" });

      const cleared = await exitTenant(
        makeRequest("http://localhost/api/admin/session", { method: "DELETE" }),
      );
      expect(cleared.headers.get("set-cookie")).toContain("Max-Age=0");
    });
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
      const response = await enterTenant(
        makeRequest("http://localhost/api/admin/tenants/acme/session", {
          method: "POST",
          body: "{}",
        }),
        params,
      );
      expect(response.status).toBe(404);
    });
  });
});
