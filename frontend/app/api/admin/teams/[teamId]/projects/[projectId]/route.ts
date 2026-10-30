import { adminServerConfig, resolveServerConfig } from "../../../../../../../lib/server/config";
import { requestLocale, serializeUpstream } from "../../../../../../../lib/server/proxy";

type Context = { params: Promise<{ teamId: string; projectId: string }> };

function adminConfig() {
  return adminServerConfig(resolveServerConfig(process.env as Record<string, string | undefined>));
}

function projectPath(teamId: string, projectId: string): string {
  return (
    "/admin/v1/teams/" +
    encodeURIComponent(teamId) +
    "/projects/" +
    encodeURIComponent(projectId)
  );
}

export async function GET(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  return serializeUpstream({
    config: adminConfig(),
    tenantScoped: false,
    request,
    upstreamPath: projectPath((await context.params).teamId, (await context.params).projectId),
    method: "GET",
    failureLabel: "admin_team_project_get_failed",
    locale,
  });
}

/**
 * Deletes a project and every bank it owns.
 *
 * The upstream demands two explicit consents before a destructive delete: the
 * project's own id as the confirmation, and delete_banks. The console has
 * already made the operator confirm the project, so this route states both
 * itself rather than forwarding whatever body arrived — a caller cannot delete
 * a project's banks by omitting them.
 */
export async function DELETE(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const { teamId, projectId } = await context.params;
  return serializeUpstream({
    config: adminConfig(),
    tenantScoped: false,
    request,
    upstreamPath: projectPath(teamId, projectId),
    method: "DELETE",
    body: JSON.stringify({ confirm: projectId, delete_banks: true }),
    failureLabel: "admin_team_project_delete_failed",
    locale,
  });
}
