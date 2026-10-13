import { resolveServerConfig } from "../../../../../../../lib/server/config";
import { bankScopedPath, proxyUpstream, searchParamsObject } from "../../../../../../../lib/server/proxy";

type Context = { params: Promise<{ bankId: string; mentalModelId: string }> };

/** Mental-model refresh child action. */
export async function POST(request: Request, context: Context): Promise<Response> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const { bankId, mentalModelId } = await context.params;
  const body = await request.text();
  return proxyUpstream({
    config,
    request,
    upstreamPath: bankScopedPath(
      "/v1/default/banks",
      bankId,
      `/mental-models/${encodeURIComponent(mentalModelId)}/refresh`,
    ),
    query: searchParamsObject(request),
    method: "POST",
    body: body.length > 0 ? body : undefined,
  });
}