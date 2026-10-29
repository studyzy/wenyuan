import { adminServerConfig, resolveServerConfig } from "../../../../../lib/server/config";
import { requestLocale, serializeUpstream } from "../../../../../lib/server/proxy";

type Context = { params: Promise<{ tenantId: string }> };

function adminConfig() {
  return adminServerConfig(resolveServerConfig(process.env as Record<string, string | undefined>));
}

function tenantPath(tenantId: string): string {
  return "/admin/v1/tenants/" + encodeURIComponent(tenantId);
}

export async function GET(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  return serializeUpstream({
    config: adminConfig(),
    tenantScoped: false,
    request,
    upstreamPath: tenantPath((await context.params).tenantId),
    method: "GET",
    failureLabel: "admin_tenant_get_failed",
    locale,
  });
}

export async function PATCH(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  return serializeUpstream({
    config: adminConfig(),
    tenantScoped: false,
    request,
    upstreamPath: tenantPath((await context.params).tenantId),
    method: "PATCH",
    body: await request.text(),
    failureLabel: "admin_tenant_update_failed",
    locale,
  });
}

export async function DELETE(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  return serializeUpstream({
    config: adminConfig(),
    tenantScoped: false,
    request,
    upstreamPath: tenantPath((await context.params).tenantId),
    method: "DELETE",
    failureLabel: "admin_tenant_delete_failed",
    locale,
  });
}
