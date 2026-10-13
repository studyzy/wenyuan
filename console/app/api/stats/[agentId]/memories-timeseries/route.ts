import { resolveServerConfig } from "../../../../../lib/server/config";
import { proxyUpstream, requestLocale, searchParamsObject } from "../../../../../lib/server/proxy";
import { errorResponse, resolveBankScoping } from "../../../../../lib/server/http";

type Context = { params: Promise<{ agentId: string }> };

/**
 * Per-bank memory time-series with period and time-field query parameters.
 */
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
    upstreamPath: `/v1/default/stats/${encodeURIComponent((await context.params).agentId)}/memories-timeseries`,
    query: searchParamsObject(request),
    method: "GET",
  });
}