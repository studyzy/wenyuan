import { resolveServerConfig } from "../../../../../lib/server/config";
import { bankScopedPath, proxyUpstream, searchParamsObject } from "../../../../../lib/server/proxy";

type Context = { params: Promise<{ bankId: string }> };

/** Bank LLM-request log listing with query passthrough. */
export async function GET(request: Request, context: Context): Promise<Response> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  return proxyUpstream({
    config,
    request,
    upstreamPath: bankScopedPath("/v1/default/banks", (await context.params).bankId, "/llm-requests"),
    query: searchParamsObject(request),
    method: "GET",
  });
}