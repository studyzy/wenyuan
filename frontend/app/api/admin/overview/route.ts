import { adminServerConfig, resolveServerConfig } from "../../../../lib/server/config";
import { requestLocale, serializeUpstream } from "../../../../lib/server/proxy";

export async function GET(request: Request): Promise<Response> {
  const locale = requestLocale(request);
  return serializeUpstream({
    config: adminServerConfig(resolveServerConfig(process.env as Record<string, string | undefined>)),
    tenantScoped: false,
    request,
    upstreamPath: "/admin/v1/overview",
    method: "GET",
    failureLabel: "admin_overview_failed",
    locale,
  });
}
