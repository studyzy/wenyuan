import { adminServerConfig, resolveServerConfig } from "../../../../../../lib/server/config";
import { requestLocale, serializeUpstream } from "../../../../../../lib/server/proxy";

type Context = { params: Promise<{ tenantId: string }> };

function adminConfig() {
  return adminServerConfig(resolveServerConfig(process.env as Record<string, string | undefined>));
}

export async function POST(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  return serializeUpstream({
    config: adminConfig(),
    tenantScoped: false,
    request,
    upstreamPath:
      "/admin/v1/tenants/" + encodeURIComponent((await context.params).tenantId) + "/migrate",
    method: "POST",
    body: await request.text(),
    failureLabel: "admin_tenant_migrate_failed",
    locale,
  });
}
