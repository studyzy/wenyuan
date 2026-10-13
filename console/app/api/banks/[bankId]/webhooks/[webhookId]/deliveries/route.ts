import { resolveServerConfig } from "../../../../../../../lib/server/config";
import { bankScopedPath, proxyUpstream, searchParamsObject } from "../../../../../../../lib/server/proxy";

type Context = { params: Promise<{ bankId: string; webhookId: string }> };

/** Webhook delivery history with paging query passthrough. */
export async function GET(request: Request, context: Context): Promise<Response> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const { bankId, webhookId } = await context.params;
  return proxyUpstream({
    config,
    request,
    upstreamPath: bankScopedPath(
      "/v1/default/banks",
      bankId,
      `/webhooks/${encodeURIComponent(webhookId)}/deliveries`,
    ),
    query: searchParamsObject(request),
    method: "GET",
  });
}