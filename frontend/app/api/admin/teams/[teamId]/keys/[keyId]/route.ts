import { adminServerConfig, resolveServerConfig } from "../../../../../../../lib/server/config";
import { requestLocale, serializeUpstream } from "../../../../../../../lib/server/proxy";

type Context = { params: Promise<{ teamId: string; keyId: string }> };

function adminConfig() {
  return adminServerConfig(resolveServerConfig(process.env as Record<string, string | undefined>));
}

function keyPath(teamId: string, keyId: string): string {
  return "/admin/v1/teams/" + encodeURIComponent(teamId) + "/keys/" + encodeURIComponent(keyId);
}

export async function DELETE(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const { teamId, keyId } = await context.params;
  return serializeUpstream({
    config: adminConfig(),
    tenantScoped: false,
    request,
    upstreamPath: keyPath(teamId, keyId),
    method: "DELETE",
    failureLabel: "admin_team_key_revoke_failed",
    locale,
  });
}
