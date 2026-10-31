import { adminServerConfig, resolveServerConfig } from "../../../../../../../lib/server/config";
import { requestLocale, serializeUpstream } from "../../../../../../../lib/server/proxy";

type Context = { params: Promise<{ tenantId: string }> };

function adminConfig() {
  return adminServerConfig(resolveServerConfig(process.env as Record<string, string | undefined>));
}

export async function POST(request: Request, context: Context): Promise<Response> {
  const tenantId = (await context.params).tenantId;
  return serializeUpstream({
    config: adminConfig(),
    tenantScoped: false,
    request,
    upstreamPath: "/admin/v1/tenants/" + encodeURIComponent(tenantId) + "/aggregate/rebuild",
    method: "POST",
    failureLabel: "admin_tenant_aggregate_rebuild_failed",
    locale: requestLocale(request),
  });
}
