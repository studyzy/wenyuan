import { resolveServerConfig } from "../../../../../lib/server/config";
import { proxyUpstream, searchParamsObject } from "../../../../../lib/server/proxy";

type Context = { params: Promise<{ nodeId: string }> };

/** Knowledge-base node sub-resource: GET reads, PATCH updates, DELETE removes. */
export async function GET(request: Request, context: Context): Promise<Response> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  return proxyUpstream({
    config,
    request,
    upstreamPath: `/v1/default/knowledge-base/nodes/${encodeURIComponent((await context.params).nodeId)}`,
    query: searchParamsObject(request),
    method: "GET",
  });
}

export async function PATCH(request: Request, context: Context): Promise<Response> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const body = await request.text();
  return proxyUpstream({
    config,
    request,
    upstreamPath: `/v1/default/knowledge-base/nodes/${encodeURIComponent((await context.params).nodeId)}`,
    method: "PATCH",
    body,
  });
}

export async function DELETE(request: Request, context: Context): Promise<Response> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  return proxyUpstream({
    config,
    request,
    upstreamPath: `/v1/default/knowledge-base/nodes/${encodeURIComponent((await context.params).nodeId)}`,
    method: "DELETE",
  });
}