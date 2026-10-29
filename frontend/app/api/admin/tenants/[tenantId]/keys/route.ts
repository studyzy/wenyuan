import { adminServerConfig, resolveServerConfig } from "../../../../../../lib/server/config";
import { requestLocale, serializeUpstream } from "../../../../../../lib/server/proxy";

type Context = { params: Promise<{ tenantId: string }> };

function adminConfig() {
  return adminServerConfig(resolveServerConfig(process.env as Record<string, string | undefined>));
}

function keysPath(tenantId: string): string {
  return "/admin/v1/tenants/" + encodeURIComponent(tenantId) + "/keys";
}

export async function GET(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  return serializeUpstream({
    config: adminConfig(),
    tenantScoped: false,
    request,
    upstreamPath: keysPath((await context.params).tenantId),
    method: "GET",
    failureLabel: "admin_keys_list_failed",
    locale,
  });
}

export async function POST(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  return serializeUpstream({
    config: adminConfig(),
    tenantScoped: false,
    request,
    upstreamPath: keysPath((await context.params).tenantId),
    method: "POST",
    body: await request.text(),
    successStatus: 201,
    failureLabel: "admin_key_create_failed",
    locale,
  });
}
