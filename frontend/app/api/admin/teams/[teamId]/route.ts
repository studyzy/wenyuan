import { adminServerConfig, resolveServerConfig } from "../../../../../lib/server/config";
import { requestLocale, serializeUpstream } from "../../../../../lib/server/proxy";

type Context = { params: Promise<{ teamId: string }> };

function adminConfig() {
  return adminServerConfig(resolveServerConfig(process.env as Record<string, string | undefined>));
}

function teamPath(teamId: string): string {
  return "/admin/v1/teams/" + encodeURIComponent(teamId);
}

export async function GET(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  return serializeUpstream({
    config: adminConfig(),
    tenantScoped: false,
    request,
    upstreamPath: teamPath((await context.params).teamId),
    method: "GET",
    failureLabel: "admin_team_get_failed",
    locale,
  });
}

export async function PATCH(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  return serializeUpstream({
    config: adminConfig(),
    tenantScoped: false,
    request,
    upstreamPath: teamPath((await context.params).teamId),
    method: "PATCH",
    body: await request.text(),
    failureLabel: "admin_team_update_failed",
    locale,
  });
}

export async function DELETE(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  return serializeUpstream({
    config: adminConfig(),
    tenantScoped: false,
    request,
    upstreamPath: teamPath((await context.params).teamId),
    method: "DELETE",
    failureLabel: "admin_team_delete_failed",
    locale,
  });
}
