import { adminServerConfig, resolveServerConfig } from "../../../../lib/server/config";
import { requestLocale, serializeUpstream } from "../../../../lib/server/proxy";

const QUERY_PARAMS = ["status", "kind", "q", "limit", "offset"] as const;

function adminConfig() {
  return adminServerConfig(resolveServerConfig(process.env as Record<string, string | undefined>));
}

function query(request: Request): Record<string, string | undefined> {
  const search = new URL(request.url).searchParams;
  const out: Record<string, string | undefined> = {};
  for (const key of QUERY_PARAMS) {
    const value = search.get(key);
    if (value !== null) out[key] = value;
  }
  return out;
}

/**
 * Tenant collection on the management plane. The browser never sees the
 * operator key: the BFF holds it and forwards the admin payload.
 */
export async function GET(request: Request): Promise<Response> {
  const locale = requestLocale(request);
  return serializeUpstream({
    config: adminConfig(),
    tenantScoped: false,
    request,
    upstreamPath: "/admin/v1/tenants",
    query: query(request),
    method: "GET",
    failureLabel: "admin_tenants_list_failed",
    locale,
  });
}

export async function POST(request: Request): Promise<Response> {
  const locale = requestLocale(request);
  return serializeUpstream({
    config: adminConfig(),
    tenantScoped: false,
    request,
    upstreamPath: "/admin/v1/tenants",
    method: "POST",
    body: await request.text(),
    successStatus: 201,
    failureLabel: "admin_tenant_create_failed",
    locale,
  });
}
