import { resolveServerConfig } from "../../../../lib/server/config";
import { proxyUpstream, requestLocale, searchParamsObject } from "../../../../lib/server/proxy";
import { errorResponse, resolveBankScoping } from "../../../../lib/server/http";

/**
 * Asynchronous retain. The route name spelling `retain_async` is fixed
 * compatibility surface.
 */
export async function POST(request: Request): Promise<Response> {
  const locale = requestLocale(request);
  const scoping = resolveBankScoping(new URL(request.url).searchParams);
  if (!scoping) {
    return errorResponse("errors.requiredBankId", 400, locale);
  }
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const body = await request.arrayBuffer();
  return proxyUpstream({
    config,
    request,
    upstreamPath: "/v1/default/memories/retain_async",
    query: searchParamsObject(request),
    method: "POST",
    body,
  });
}