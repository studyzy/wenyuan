import { resolveServerConfig } from "../../../../lib/server/config";
import { proxyUpstream, searchParamsObject } from "../../../../lib/server/proxy";

type Context = { params: Promise<{ agentId: string }> };

/**
 * Operation queue keyed by bank id: GET reads, DELETE removes.
 */
export async function GET(request: Request, context: Context): Promise<Response> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  return proxyUpstream({
    config,
    request,
    upstreamPath: `/v1/default/operations/${encodeURIComponent((await context.params).agentId)}`,
    query: searchParamsObject(request),
    method: "GET",
  });
}

export async function DELETE(request: Request, context: Context): Promise<Response> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  return proxyUpstream({
    config,
    request,
    upstreamPath: `/v1/default/operations/${encodeURIComponent((await context.params).agentId)}`,
    query: searchParamsObject(request),
    method: "DELETE",
  });
}