import { adminServerConfig, resolveServerConfig } from "../../../../../../../../lib/server/config";
import { requestLocale, serializeUpstream } from "../../../../../../../../lib/server/proxy";

type Context = { params: Promise<{ teamId: string; projectId: string }> };

function adminConfig() {
  return adminServerConfig(resolveServerConfig(process.env as Record<string, string | undefined>));
}

function projectMembersPath(teamId: string, projectId: string): string {
  return (
    "/admin/v1/teams/" +
    encodeURIComponent(teamId) +
    "/projects/" +
    encodeURIComponent(projectId) +
    "/members"
  );
}

export async function GET(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  return serializeUpstream({
    config: adminConfig(),
    tenantScoped: false,
    request,
    upstreamPath: projectMembersPath((await context.params).teamId, (await context.params).projectId),
    method: "GET",
    failureLabel: "admin_project_members_list_failed",
    locale,
  });
}
