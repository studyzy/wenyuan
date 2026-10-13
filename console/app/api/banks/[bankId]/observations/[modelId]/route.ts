import { resolveServerConfig } from "../../../../../../lib/server/config";
import { bankScopedPath, proxyUpstream, searchParamsObject } from "../../../../../../lib/server/proxy";

type Context = { params: Promise<{ bankId: string; modelId: string }> };

/** Per-model observation read. */
export async function GET(request: Request, context: Context): Promise<Response> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const { bankId, modelId } = await context.params;
  return proxyUpstream({
    config,
    request,
    upstreamPath: bankScopedPath(
      "/v1/default/banks",
      bankId,
      `/observations/${encodeURIComponent(modelId)}`,
    ),
    query: searchParamsObject(request),
    method: "GET",
  });
}