import { resolveServerConfig } from "../../../../lib/server/config";
import { proxyUpstream, searchParamsObject } from "../../../../lib/server/proxy";

/** Knowledge-base search for the search dialog. */
export async function GET(request: Request): Promise<Response> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  return proxyUpstream({
    config,
    request,
    upstreamPath: "/v1/default/knowledge-base/search",
    query: searchParamsObject(request),
    method: "GET",
  });
}