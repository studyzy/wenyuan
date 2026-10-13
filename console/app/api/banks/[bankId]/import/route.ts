import { resolveServerConfig } from "../../../../../lib/server/config";
import { bankScopedPath, proxyUpstream, searchParamsObject } from "../../../../../lib/server/proxy";

type Context = { params: Promise<{ bankId: string }> };

/**
 * Bank template import. The optional dry-run query flag is passed through to
 * the upstream unchanged.
 */
export async function POST(request: Request, context: Context): Promise<Response> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const body = await request.text();
  return proxyUpstream({
    config,
    request,
    upstreamPath: bankScopedPath("/v1/default/banks", (await context.params).bankId, "/import"),
    query: searchParamsObject(request),
    method: "POST",
    body,
  });
}