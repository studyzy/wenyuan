import { adminServerConfig, resolveServerConfig } from "../../../../../../lib/server/config";
import { requestLocale, serializeUpstream } from "../../../../../../lib/server/proxy";

type Context = { params: Promise<{ teamId: string }> };

function adminConfig() {
  return adminServerConfig(resolveServerConfig(process.env as Record<string, string | undefined>));
}

function projectsPath(teamId: string): string {
  return "/admin/v1/teams/" + encodeURIComponent(teamId) + "/projects";
}

export async function GET(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  return serializeUpstream({
    config: adminConfig(),
    tenantScoped: false,
    request,
    upstreamPath: projectsPath((await context.params).teamId),
    method: "GET",
    failureLabel: "admin_team_projects_list_failed",
    locale,
  });
}

export async function POST(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  return serializeUpstream({
    config: adminConfig(),
    tenantScoped: false,
    request,
    upstreamPath: projectsPath((await context.params).teamId),
    method: "POST",
    body: await request.text(),
    successStatus: 201,
    failureLabel: "admin_team_project_create_failed",
    locale,
  });
}
