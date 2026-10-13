import { resolveServerConfig } from "../../../../../lib/server/config";
import { proxyUpstream, requestLocale, searchParamsObject } from "../../../../../lib/server/proxy";
import { errorResponse, resolveBankScoping } from "../../../../../lib/server/http";

type Context = { params: Promise<{ pageId: string }> };

/** Knowledge-base per-page read with bank scoping in the query string. */
export async function GET(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const scoping = resolveBankScoping(new URL(request.url).searchParams);
  if (!scoping) {
    return errorResponse("errors.requiredBankId", 400, locale);
  }
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  return proxyUpstream({
    config,
    request,
    upstreamPath: `/v1/default/knowledge-base/pages/${encodeURIComponent((await context.params).pageId)}`,
    query: searchParamsObject(request),
    method: "GET",
  });
}