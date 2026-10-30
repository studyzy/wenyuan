import { adminServerConfig, resolveServerConfig } from "../../../../../../../lib/server/config";
import { requestLocale, serializeUpstream } from "../../../../../../../lib/server/proxy";

type Context = { params: Promise<{ teamId: string; tenantId: string }> };

function adminConfig() {
  return adminServerConfig(resolveServerConfig(process.env as Record<string, string | undefined>));
}

function memberPath(teamId: string, tenantId: string): string {
  return "/admin/v1/teams/" + encodeURIComponent(teamId) + "/members/" + encodeURIComponent(tenantId);
}

export async function PUT(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  return serializeUpstream({
    config: adminConfig(),
    tenantScoped: false,
    request,
    upstreamPath: memberPath((await context.params).teamId, (await context.params).tenantId),
    method: "PUT",
    body: await request.text(),
    failureLabel: "admin_team_member_add_failed",
    locale,
  });
}

export async function DELETE(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  return serializeUpstream({
    config: adminConfig(),
    tenantScoped: false,
    request,
    upstreamPath: memberPath((await context.params).teamId, (await context.params).tenantId),
    method: "DELETE",
    failureLabel: "admin_team_member_remove_failed",
    locale,
  });
}
