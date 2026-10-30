import { afterEach, describe, expect, it } from "vitest";
import { POST as login } from "../app/api/auth/login/route";
import { POST as logout } from "../app/api/auth/logout/route";
import { stubFetch, withEnv } from "./support/fixtures";
import { expectNoErrorKey, makeRequest, readJson } from "./support/harness";

const OPERATOR_KEY = "operator-secret";
const DATA_KEY = "data-secret";

afterEach(() => {
  delete process.env.WENYUAN_CP_ADMIN_API_KEY;
  delete process.env.WENYUAN_CP_DATAPLANE_API_KEY;
});

function signIn(key: string, extra: Record<string, string> = {}) {
  return makeRequest("http://localhost/api/auth/login", {
    method: "POST",
    body: JSON.stringify({ key }),
    ...extra,
  });
}

/** Subject the issued session cookie carries, read the way the gate reads it. */
function subjectOf(setCookie: string): string {
  const value = setCookie.split(";")[0].split("=").slice(1).join("=");
  const encoded = value.slice(0, value.lastIndexOf("."));
  return (JSON.parse(Buffer.from(encoded, "base64url").toString()) as { sub: string }).sub;
}

describe("POST /api/auth/login", () => {
  it("returns 503 when no credential is configured", async () => {
    await withEnv({ WENYUAN_CP_ADMIN_API_KEY: undefined, WENYUAN_CP_DATAPLANE_API_KEY: undefined }, async () => {
      const response = await login(signIn("x"));
      expect(response.status).toBe(503);
      expectNoErrorKey(await readJson(response));
    });
  });

  it("returns 400 on a malformed body", async () => {
    await withEnv({ WENYUAN_CP_ADMIN_API_KEY: OPERATOR_KEY }, async () => {
      const response = await login(
        makeRequest("http://localhost/api/auth/login", { method: "POST", body: "not-json" }),
      );
      expect(response.status).toBe(400);
      expectNoErrorKey(await readJson(response));
    });
  });

  it("returns 401 for a wrong key", async () => {
    await withEnv({ WENYUAN_CP_ADMIN_API_KEY: OPERATOR_KEY }, async () => {
      const response = await login(signIn("wrong"));
      expect(response.status).toBe(401);
      expectNoErrorKey(await readJson(response));
    });
  });

  it("opens an operator session for the management-plane key", async () => {
    const stub = stubFetch([{ status: 200, body: {} }]);
    try {
      await withEnv({ WENYUAN_CP_ADMIN_API_KEY: OPERATOR_KEY, WENYUAN_CP_DATAPLANE_API_KEY: DATA_KEY }, async () => {
        const response = await login(signIn(OPERATOR_KEY));
        expect(response.status).toBe(200);
        const cookie = response.headers.getSetCookie()[0] ?? "";
        expect(cookie).toContain("wenyuan_cp_access=");
        expect(cookie).toContain("Max-Age=86400");
        expect(cookie).toContain("HttpOnly");
        expect(cookie).toContain("SameSite=Lax");
        expect(subjectOf(cookie)).toBe("console");
      });
      // The operator credential is never sent upstream at sign-in.
      expect(stub.calls).toHaveLength(0);
    } finally {
      stub.restore();
    }
  });

  it("opens a data session for the API key once the API accepts it", async () => {
    const stub = stubFetch([{ status: 200, body: { banks: [] } }]);
    try {
      await withEnv({ WENYUAN_CP_ADMIN_API_KEY: OPERATOR_KEY, WENYUAN_CP_DATAPLANE_API_KEY: DATA_KEY }, async () => {
        const response = await login(signIn(DATA_KEY));
        expect(response.status).toBe(200);
        const cookies = response.headers.getSetCookie();
        expect(subjectOf(cookies[0] ?? "")).toBe("data");
        // The console proxies with the configured data key, so no tenant cookie
        // is needed: the credential never leaves the server.
        expect(cookies.some((c) => c.startsWith("wenyuan_cp_tenant="))).toBe(false);
      });
      expect(stub.calls[0]).toContain("/v1/default/banks");
    } finally {
      stub.restore();
    }
  });

  it("rejects the data key when the API refuses it", async () => {
    // Multi-tenant deployments ignore the static key; signing in with it would
    // otherwise open a console whose every page answers 401.
    const stub = stubFetch([{ status: 401, body: { detail: "Authentication failed: Invalid API key" } }]);
    try {
      await withEnv({ WENYUAN_CP_ADMIN_API_KEY: OPERATOR_KEY, WENYUAN_CP_DATAPLANE_API_KEY: DATA_KEY }, async () => {
        const response = await login(signIn(DATA_KEY));
        expect(response.status).toBe(401);
        expectNoErrorKey(await readJson(response));
      });
    } finally {
      stub.restore();
    }
  });

  it("ignores a submitted key that arrives under another field name", async () => {
    // The login form posts `key`; a route that reads a different name answers
    // 401 to every sign-in while its own tests, which post that other name,
    // still pass. The field name is the contract between the two.
    await withEnv({ WENYUAN_CP_ADMIN_API_KEY: OPERATOR_KEY }, async () => {
      const response = await login(
        makeRequest("http://localhost/api/auth/login", {
          method: "POST",
          body: JSON.stringify({ accessKey: OPERATOR_KEY }),
        }),
      );
      expect(response.status).toBe(401);
      expectNoErrorKey(await readJson(response));
    });
  });

  it("marks the cookie secure only for an HTTPS-originated request", async () => {
    await withEnv({ WENYUAN_CP_ADMIN_API_KEY: OPERATOR_KEY }, async () => {
      const secure = await login(signIn(OPERATOR_KEY, { forwardedProto: "https" }));
      expect(secure.headers.get("set-cookie")).toContain("Secure");

      const plain = await login(signIn(OPERATOR_KEY, { forwardedProto: "http" }));
      expect(plain.headers.get("set-cookie")).not.toContain("Secure");
    });
  });
});

describe("tenant key sign-in", () => {
  const tenantEnv = { WENYUAN_CP_ADMIN_API_KEY: OPERATOR_KEY };
  const tenantBody = { key: "wyk_tenantkey" };

  it("enters the tenant when the management plane resolves the key", async () => {
    const stub = stubFetch([{ status: 200, body: { tenant_id: "studyzy" } }]);
    try {
      await withEnv(tenantEnv, async () => {
        const response = await login(
          makeRequest("http://localhost/api/auth/login", {
            method: "POST",
            body: JSON.stringify(tenantBody),
          }),
        );
        expect(response.status).toBe(200);
        expect(await readJson(response)).toMatchObject({ signedIn: true, tenant_id: "studyzy" });
        const cookies = response.headers.getSetCookie();
        expect(subjectOf(cookies[0] ?? "")).toBe("tenant:studyzy");
        expect(cookies.some((c) => c.startsWith("wenyuan_cp_tenant="))).toBe(true);
      });
      expect(stub.calls[0]).toContain("/admin/v1/tenants/resolve");
      expect(stub.bodies[0]).toContain("wyk_tenantkey");
    } finally {
      stub.restore();
    }
  });

  it("rejects a tenant key the registry does not know", async () => {
    const stub = stubFetch([{ status: 401, body: { error: "authentication failed" } }]);
    try {
      await withEnv(tenantEnv, async () => {
        const response = await login(
          makeRequest("http://localhost/api/auth/login", {
            method: "POST",
            body: JSON.stringify(tenantBody),
          }),
        );
        expect(response.status).toBe(401);
        expectNoErrorKey(await readJson(response));
        expect(response.headers.getSetCookie().length).toBe(0);
      });
    } finally {
      stub.restore();
    }
  });

  it("reports tenant sign-in as unavailable when the deployment cannot resolve keys", async () => {
    // Single-tenant deployments answer 501: the data key still works, but a
    // tenant key must not be mistaken for a wrong password.
    const stub = stubFetch([{ status: 501, body: { error: "tenant key resolution is not configured" } }]);
    try {
      await withEnv(tenantEnv, async () => {
        const response = await login(
          makeRequest("http://localhost/api/auth/login", {
            method: "POST",
            body: JSON.stringify(tenantBody),
          }),
        );
        expect(response.status).toBe(503);
        expectNoErrorKey(await readJson(response));
      });
    } finally {
      stub.restore();
    }
  });

  it("never asks the management plane about a key that is not a tenant key", async () => {
    const stub = stubFetch([{ status: 200, body: { tenant_id: "studyzy" } }]);
    try {
      await withEnv(tenantEnv, async () => {
        const response = await login(
          makeRequest("http://localhost/api/auth/login", {
            method: "POST",
            body: JSON.stringify({ key: "not-a-tenant-key" }),
          }),
        );
        expect(response.status).toBe(401);
        expect(stub.calls).toHaveLength(0);
      });
    } finally {
      stub.restore();
    }
  });
});

describe("POST /api/auth/logout", () => {
  it("clears the console and tenant cookies with max age zero", async () => {
    const response = await logout(
      makeRequest("http://localhost/api/auth/logout", { method: "POST" }),
    );
    expect(response.status).toBe(200);
    const cookies = response.headers.getSetCookie();
    expect(cookies.some((c) => c.startsWith("wenyuan_cp_access=") && c.includes("Max-Age=0"))).toBe(true);
    expect(cookies.some((c) => c.startsWith("wenyuan_cp_tenant=") && c.includes("Max-Age=0"))).toBe(true);
  });
});
