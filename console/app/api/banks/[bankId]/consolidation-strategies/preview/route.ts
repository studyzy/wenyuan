import { resolveServerConfig } from "../../../../../../lib/server/config";
import { bankScopedPath, proxyUpstream } from "../../../../../../lib/server/proxy";

type Context = { params: Promise<{ bankId: string }> };

/** Consolidation-strategy preview for the strategy editor. */
export async function POST(request: Request, context: Context): Promise<Response> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const body = await request.text();
  return proxyUpstream({
    config,
    request,
    upstreamPath: bankScopedPath(
      "/v1/default/banks",
      (await context.params).bankId,
      "/consolidation-strategies/preview",
    ),
    method: "POST",
    body,
  });
}