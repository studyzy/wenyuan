import { resolveServerConfig } from "../../../../lib/server/config";
import { proxyUpstream, searchParamsObject } from "../../../../lib/server/proxy";

type Context = { params: Promise<{ agentId: string }> };

/** Per-bank statistics and its memories-timeseries child. */
export async function GET(request: Request, context: Context): Promise<Response> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  return proxyUpstream({
    config,
    request,
    upstreamPath: `/v1/default/stats/${encodeURIComponent((await context.params).agentId)}`,
    query: searchParamsObject(request),
    method: "GET",
  });
}