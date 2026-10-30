import { adminServerConfig, resolveServerConfig } from "../../../../../../lib/server/config";
import { requestLocale, serializeUpstream } from "../../../../../../lib/server/proxy";

type Context = { params: Promise<{ teamId: string }> };

function adminConfig() {
  return adminServerConfig(resolveServerConfig(process.env as Record<string, string | undefined>));
}

function membersPath(teamId: string): string {
  return "/admin/v1/teams/" + encodeURIComponent(teamId) + "/members";
}

export async function GET(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  return serializeUpstream({
    config: adminConfig(),
    tenantScoped: false,
    request,
    upstreamPath: membersPath((await context.params).teamId),
    method: "GET",
    failureLabel: "admin_team_members_list_failed",
    locale,
  });
}
