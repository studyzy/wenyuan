import { withBasePath } from "./base-path";

export type TenantStatus = "provisioning" | "active" | "suspended" | "deleted" | string;

export type Tenant = {
  tenant_id: string;
  display_name: string;
  status: TenantStatus;
  config?: Record<string, unknown>;
  created_at?: string;
  updated_at?: string;
  deleted_at?: string;
};

export type TenantKey = {
  key_id: string;
  prefix: string;
  label: string;
  ephemeral: boolean;
  expires_at?: string;
  revoked_at?: string;
  last_used_at?: string;
  created_at?: string;
};

export type IssuedKey = { plaintext: string; key: TenantKey };

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(withBasePath(path), {
    ...init,
    headers: { "content-type": "application/json", ...(init?.headers ?? {}) },
  });
  const text = await response.text();
  let body: Record<string, unknown> = {};
  try {
    body = text ? (JSON.parse(text) as Record<string, unknown>) : {};
  } catch {
    body = {};
  }
  if (!response.ok) {
    const message = body.message ?? body.details ?? body.error;
    throw new Error(typeof message === "string" ? message : "Request failed (" + response.status + ")");
  }
  return body as T;
}

export const adminApi = {
  listTenants(params: { status?: string; q?: string; limit?: number; offset?: number } = {}) {
    const query = new URLSearchParams();
    if (params.status) query.set("status", params.status);
    if (params.q) query.set("q", params.q);
    if (params.limit) query.set("limit", String(params.limit));
    if (params.offset) query.set("offset", String(params.offset));
    const suffix = query.toString();
    return request<{ tenants: Tenant[] }>("/api/admin/tenants" + (suffix ? "?" + suffix : ""));
  },
  createTenant(input: {
    tenant_id: string;
    display_name?: string;
    config?: Record<string, unknown>;
    adopt?: boolean;
  }) {
    return request<Tenant>("/api/admin/tenants", {
      method: "POST",
      body: JSON.stringify(input),
    });
  },
  getTenant(tenantId: string) {
    return request<Tenant>("/api/admin/tenants/" + encodeURIComponent(tenantId));
  },
  updateTenant(
    tenantId: string,
    patch: { display_name?: string; config?: Record<string, unknown>; status?: string },
  ) {
    return request<Tenant>("/api/admin/tenants/" + encodeURIComponent(tenantId), {
      method: "PATCH",
      body: JSON.stringify(patch),
    });
  },
  deleteTenant(tenantId: string) {
    return request<Tenant>("/api/admin/tenants/" + encodeURIComponent(tenantId), {
      method: "DELETE",
    });
  },
  listKeys(tenantId: string) {
    return request<{ keys: TenantKey[] }>(
      "/api/admin/tenants/" + encodeURIComponent(tenantId) + "/keys",
    );
  },
  createKey(
    tenantId: string,
    input: { label?: string; expires_at?: string; ephemeral?: boolean } = {},
  ) {
    return request<IssuedKey>(
      "/api/admin/tenants/" + encodeURIComponent(tenantId) + "/keys",
      { method: "POST", body: JSON.stringify(input) },
    );
  },
  enterTenant(tenantId: string) {
    return request<{ tenant_id: string }>(
      "/api/admin/tenants/" + encodeURIComponent(tenantId) + "/session",
      { method: "POST", body: "{}" },
    );
  },
  purgeTenant(tenantId: string, confirm: string) {
    return request<{ tenant_id: string; purged: boolean }>(
      "/api/admin/tenants/" + encodeURIComponent(tenantId) + "/purge",
      { method: "POST", body: JSON.stringify({ confirm }) },
    );
  },
  migrateTenant(tenantId: string) {
    return request<{ status: string }>(
      "/api/admin/tenants/" + encodeURIComponent(tenantId) + "/migrate",
      { method: "POST", body: "{}" },
    );
  },
  tenantSession() {
    return request<{ tenant_id: string | null }>("/api/admin/session");
  },
  exitTenant() {
    return request<{ tenant_id: null }>("/api/admin/session", { method: "DELETE" });
  },
  revokeKey(tenantId: string, keyId: string) {
    return request<{ status: string }>(
      "/api/admin/tenants/" +
        encodeURIComponent(tenantId) +
        "/keys/" +
        encodeURIComponent(keyId),
      { method: "DELETE" },
    );
  },
  overview() {
    return request<Record<string, number>>("/api/admin/overview");
  },
};
