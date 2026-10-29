import { describe, expect, it } from "vitest";
import { serializeUpstream } from "../lib/server/proxy";
import { createTenantSessionToken } from "../lib/server/session";
import { configFixture, stubFetch } from "./support/fixtures";
import { makeRequest, readJson } from "./support/harness";

const config = configFixture({
  accessKey: "console-secret",
  dataplaneUrl: "http://data.local",
  dataplaneKey: "static-key",
  adminUrl: "http://admin.local",
  adminKey: "operator-key",
});

const NOW = Date.now();

function tenantCookie(key: string, now = NOW): string {
  return "wenyuan_cp_tenant=" + createTenantSessionToken("console-secret", "acme", key, now);
}

describe("tenant-scoped data-plane proxy", () => {
  it("uses the static key when no tenant session exists", async () => {
    const stub = stubFetch([{ body: {} }]);
    try {
      await serializeUpstream({
        config,
        request: makeRequest("http://localhost/api/banks"),
        upstreamPath: "/v1/default/banks",
        failureLabel: "x",
      });
      expect(new Headers(stub.headers[0] as HeadersInit).get("authorization")).toBe("Bearer static-key");
    } finally {
      stub.restore();
    }
  });

  it("uses the tenant key when a session cookie is present", async () => {
    const stub = stubFetch([{ body: {} }]);
    try {
      await serializeUpstream({
        config,
        request: makeRequest("http://localhost/api/banks", { cookie: tenantCookie("wyk_tenant") }),
        upstreamPath: "/v1/default/banks",
        failureLabel: "x",
      });
      expect(new Headers(stub.headers[0] as HeadersInit).get("authorization")).toBe("Bearer wyk_tenant");
    } finally {
      stub.restore();
    }
  });

  it("fails closed on a tampered session cookie without calling upstream", async () => {
    const stub = stubFetch([{ body: {} }]);
    try {
      const response = await serializeUpstream({
        config,
        request: makeRequest("http://localhost/api/banks", {
          cookie: "wenyuan_cp_tenant=tampered.token",
        }),
        upstreamPath: "/v1/default/banks",
        failureLabel: "x",
      });
      expect(response.status).toBe(401);
      expect(stub.calls).toHaveLength(0);
    } finally {
      stub.restore();
    }
  });

  it("does not apply a tenant cookie to an admin-scoped call", async () => {
    const stub = stubFetch([{ body: {} }]);
    try {
      await serializeUpstream({
        config,
        request: makeRequest("http://localhost/api/admin/tenants", {
          cookie: tenantCookie("wyk_tenant"),
        }),
        upstreamPath: "/admin/v1/tenants",
        failureLabel: "x",
        tenantScoped: false,
      });
      expect(new Headers(stub.headers[0] as HeadersInit).get("authorization")).toBe("Bearer static-key");
    } finally {
      stub.restore();
    }
  });

  it("renews a session close to expiry and sets the rotated cookie", async () => {
    const stub = stubFetch([
      { status: 201, body: { plaintext: "wyk_renewed" } },
      { body: {} },
    ]);
    try {
      const response = await serializeUpstream({
        config,
        request: makeRequest("http://localhost/api/banks", {
          cookie: tenantCookie("wyk_old", NOW - 580_000),
        }),
        upstreamPath: "/v1/default/banks",
        failureLabel: "x",
      });
      expect(stub.calls).toEqual([
        "http://admin.local/admin/v1/tenants/acme/session",
        "http://data.local/v1/default/banks",
      ]);
      expect(new Headers(stub.headers[1] as HeadersInit).get("authorization")).toBe("Bearer wyk_renewed");
      expect(response.headers.get("set-cookie")).toContain("wenyuan_cp_tenant=");
    } finally {
      stub.restore();
    }
  });
});
