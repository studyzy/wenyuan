import { resolveServerConfig } from "../../../../../../lib/server/config";
import { bankScopedPath, proxyUpstream, searchParamsObject } from "../../../../../../lib/server/proxy";

type Context = { params: Promise<{ bankId: string }> };

/** Bank audit-log statistics with query passthrough. */
export async function GET(request: Request, context: Context): Promise<Response> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  return proxyUpstream({
    config,
    request,
    upstreamPath: bankScopedPath(
      "/v1/default/banks",
      (await context.params).bankId,
      "/audit-logs/stats",
    ),
    query: searchParamsObject(request),
    method: "GET",
  });
}