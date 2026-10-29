import { adminServerConfig, resolveServerConfig } from "../../../../../../../lib/server/config";
import { requestLocale, serializeUpstream } from "../../../../../../../lib/server/proxy";

type Context = { params: Promise<{ tenantId: string; keyId: string }> };

function adminConfig() {
  return adminServerConfig(resolveServerConfig(process.env as Record<string, string | undefined>));
}

export async function DELETE(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const params = await context.params;
  return serializeUpstream({
    config: adminConfig(),
    tenantScoped: false,
    request,
    upstreamPath:
      "/admin/v1/tenants/" +
      encodeURIComponent(params.tenantId) +
      "/keys/" +
      encodeURIComponent(params.keyId),
    method: "DELETE",
    failureLabel: "admin_key_revoke_failed",
    locale,
  });
}
