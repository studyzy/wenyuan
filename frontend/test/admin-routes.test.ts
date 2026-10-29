import { describe, expect, it } from "vitest";
import { GET as listTenants, POST as createTenant } from "../app/api/admin/tenants/route";
import {
  DELETE as deleteTenant,
  GET as getTenant,
  PATCH as patchTenant,
} from "../app/api/admin/tenants/[tenantId]/route";
import { GET as listKeys, POST as createKey } from "../app/api/admin/tenants/[tenantId]/keys/route";
import { DELETE as revokeKey } from "../app/api/admin/tenants/[tenantId]/keys/[keyId]/route";
import { GET as overview } from "../app/api/admin/overview/route";
import { POST as purgeTenant } from "../app/api/admin/tenants/[tenantId]/purge/route";
import { POST as migrateTenant } from "../app/api/admin/tenants/[tenantId]/migrate/route";
import { stubFetch, withEnv } from "./support/fixtures";
import { makeRequest, readJson } from "./support/harness";

const ADMIN_ENV = {
  WENYUAN_CP_ADMIN_API_URL: "http://admin.internal:18890",
  WENYUAN_CP_ADMIN_API_KEY: "operator-key",
};

const tenantParams = (tenantId: string) => ({ params: Promise.resolve({ tenantId }) });

async function runWithStub(
  responses: Array<{ status?: number; body?: unknown; reject?: boolean }>,
  run: (stub: ReturnType<typeof stubFetch>) => Promise<void>,
): Promise<void> {
  await withEnv(ADMIN_ENV, async () => {
    const stub = stubFetch(responses);
    try {
      await run(stub);
    } finally {
      stub.restore();
    }
  });
}

describe("admin BFF", () => {
  it("forwards the tenant list to the management plane with the operator key", async () => {
    await runWithStub([{ body: { tenants: [{ tenant_id: "acme", status: "active" }] } }], async (stub) => {
      const response = await listTenants(
        makeRequest("http://localhost/api/admin/tenants?status=active&q=ac"),
      );
      expect(response.status).toBe(200);
      expect(stub.calls[0]).toBe(
        "http://admin.internal:18890/admin/v1/tenants?status=active&q=ac",
      );
      const headers = new Headers(stub.headers[0] as HeadersInit);
      expect(headers.get("authorization")).toBe("Bearer operator-key");
      expect(await readJson(response)).toEqual({
        tenants: [{ tenant_id: "acme", status: "active" }],
      });
    });
  });

  it("creates a tenant with the upstream 201", async () => {
    await runWithStub([{ status: 201, body: { tenant_id: "acme", status: "active" } }], async (stub) => {
      const response = await createTenant(
        makeRequest("http://localhost/api/admin/tenants", {
          method: "POST",
          body: JSON.stringify({ tenant_id: "acme", display_name: "Acme" }),
        }),
      );
      expect(response.status).toBe(201);
      expect(stub.methods[0]).toBe("POST");
      expect(stub.bodies[0]).toContain('"tenant_id":"acme"');
    });
  });

  it("encodes the tenant id on detail, patch and delete", async () => {
    await runWithStub([{ body: { tenant_id: "a/b" } }, { body: { tenant_id: "a/b" } }, { body: { tenant_id: "a/b" } }], async (stub) => {
      await getTenant(makeRequest("http://localhost/api/admin/tenants/a%2Fb"), tenantParams("a/b"));
      await patchTenant(
        makeRequest("http://localhost/api/admin/tenants/a%2Fb", {
          method: "PATCH",
          body: JSON.stringify({ status: "suspended" }),
        }),
        tenantParams("a/b"),
      );
      await deleteTenant(
        makeRequest("http://localhost/api/admin/tenants/a%2Fb", { method: "DELETE" }),
        tenantParams("a/b"),
      );
      expect(stub.calls).toEqual([
        "http://admin.internal:18890/admin/v1/tenants/a%2Fb",
        "http://admin.internal:18890/admin/v1/tenants/a%2Fb",
        "http://admin.internal:18890/admin/v1/tenants/a%2Fb",
      ]);
      expect(stub.methods).toEqual(["GET", "PATCH", "DELETE"]);
    });
  });

  it("issues and revokes tenant keys without exposing them to the browser", async () => {
    await runWithStub([{ status: 201, body: { plaintext: "wyk_secret", key: { key_id: "k1" } } }, { body: { status: "revoked" } }], async (stub) => {
      const issued = await createKey(
        makeRequest("http://localhost/api/admin/tenants/acme/keys", {
          method: "POST",
          body: JSON.stringify({ label: "ci" }),
        }),
        tenantParams("acme"),
      );
      expect(issued.status).toBe(201);
      const revoked = await revokeKey(
        makeRequest("http://localhost/api/admin/tenants/acme/keys/k1", { method: "DELETE" }),
        { params: Promise.resolve({ tenantId: "acme", keyId: "k1" }) },
      );
      expect(revoked.status).toBe(200);
      expect(stub.calls).toEqual([
        "http://admin.internal:18890/admin/v1/tenants/acme/keys",
        "http://admin.internal:18890/admin/v1/tenants/acme/keys/k1",
      ]);
    });
  });

  it("forwards the overview counts", async () => {
    await runWithStub([{ body: { total: 2, active: 2 } }], async () => {
      const response = await overview(makeRequest("http://localhost/api/admin/overview"));
      expect(response.status).toBe(200);
      expect(await readJson(response)).toEqual({ total: 2, active: 2 });
    });
  });

  it("passes an upstream conflict through", async () => {
    await runWithStub([{ status: 409, body: { error: "schema already exists" } }], async () => {
      const response = await createTenant(
        makeRequest("http://localhost/api/admin/tenants", {
          method: "POST",
          body: JSON.stringify({ tenant_id: "acme" }),
        }),
      );
      expect(response.status).toBe(409);
      const body = await readJson(response);
      expect(body.error).toBe("admin_tenant_create_failed");
    });
  });
});

describe("admin purge and migrate", () => {
  it("forwards purge with the confirmation body", async () => {
    await runWithStub([{ body: { tenant_id: "acme", purged: true } }], async (stub) => {
      const response = await purgeTenant(
        makeRequest("http://localhost/api/admin/tenants/acme/purge", {
          method: "POST",
          body: JSON.stringify({ confirm: "acme" }),
        }),
        tenantParams("acme"),
      );
      expect(response.status).toBe(200);
      expect(stub.calls[0]).toBe("http://admin.internal:18890/admin/v1/tenants/acme/purge");
      expect(stub.methods[0]).toBe("POST");
      expect(stub.bodies[0]).toContain('"confirm":"acme"');
    });
  });

  it("forwards migrate", async () => {
    await runWithStub([{ body: { status: "migrated" } }], async (stub) => {
      const response = await migrateTenant(
        makeRequest("http://localhost/api/admin/tenants/acme/migrate", {
          method: "POST",
          body: "{}",
        }),
        tenantParams("acme"),
      );
      expect(response.status).toBe(200);
      expect(stub.calls[0]).toBe("http://admin.internal:18890/admin/v1/tenants/acme/migrate");
    });
  });
});
