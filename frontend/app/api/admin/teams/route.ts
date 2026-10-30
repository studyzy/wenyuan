import { adminServerConfig, resolveServerConfig } from "../../../../lib/server/config";
import { requestLocale, serializeUpstream } from "../../../../lib/server/proxy";

function adminConfig() {
  return adminServerConfig(resolveServerConfig(process.env as Record<string, string | undefined>));
}

function query(request: Request): Record<string, string | undefined> {
  const search = new URL(request.url).searchParams;
  const out: Record<string, string | undefined> = {};
  for (const key of ["q", "limit", "offset"] as const) {
    const value = search.get(key);
    if (value !== null) out[key] = value;
  }
  return out;
}

export async function GET(request: Request): Promise<Response> {
  const locale = requestLocale(request);
  return serializeUpstream({
    config: adminConfig(),
    tenantScoped: false,
    request,
    upstreamPath: "/admin/v1/teams",
    query: query(request),
    method: "GET",
    failureLabel: "admin_teams_list_failed",
    locale,
  });
}

export async function POST(request: Request): Promise<Response> {
  const locale = requestLocale(request);
  return serializeUpstream({
    config: adminConfig(),
    tenantScoped: false,
    request,
    upstreamPath: "/admin/v1/teams",
    method: "POST",
    body: await request.text(),
    successStatus: 201,
    failureLabel: "admin_team_create_failed",
    locale,
  });
}
