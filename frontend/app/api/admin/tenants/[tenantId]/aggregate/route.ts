import { adminServerConfig, resolveServerConfig } from "../../../../../../lib/server/config";
import { requestLocale, serializeUpstream } from "../../../../../../lib/server/proxy";

type Context = { params: Promise<{ tenantId: string }> };

function adminConfig() {
  return adminServerConfig(resolveServerConfig(process.env as Record<string, string | undefined>));
}

function aggregatePath(tenantId: string): string {
  return "/admin/v1/tenants/" + encodeURIComponent(tenantId) + "/aggregate";
}

export async function GET(request: Request, context: Context): Promise<Response> {
  return serializeUpstream({
    config: adminConfig(),
    tenantScoped: false,
    request,
    upstreamPath: aggregatePath((await context.params).tenantId),
    method: "GET",
    failureLabel: "admin_tenant_aggregate_failed",
    locale: requestLocale(request),
  });
}
