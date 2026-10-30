import { adminServerConfig, resolveServerConfig } from "../../../../../../../../lib/server/config";
import { requestLocale, serializeUpstream } from "../../../../../../../../lib/server/proxy";

type Context = { params: Promise<{ teamId: string; projectId: string }> };

function adminConfig() {
  return adminServerConfig(resolveServerConfig(process.env as Record<string, string | undefined>));
}

function purgePath(teamId: string, projectId: string): string {
  return (
    "/admin/v1/teams/" +
    encodeURIComponent(teamId) +
    "/projects/" +
    encodeURIComponent(projectId) +
    "/purge-contribution"
  );
}

export async function POST(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  return serializeUpstream({
    config: adminConfig(),
    tenantScoped: false,
    request,
    upstreamPath: purgePath((await context.params).teamId, (await context.params).projectId),
    method: "POST",
    body: await request.text(),
    failureLabel: "admin_project_purge_failed",
    locale,
  });
}
