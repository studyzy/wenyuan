import { resolveServerConfig } from "../../../../../lib/server/config";
import { proxyUpstream, requestLocale, searchParamsObject } from "../../../../../lib/server/proxy";
import { errorResponse, resolveBankScoping } from "../../../../../lib/server/http";

type Context = { params: Promise<{ memoryId: string }> };

/**
 * Per-memory history child.
 *
 * An upstream 404 is translated into a localized not-found payload; any other
 * upstream failure surfaces as a localized 500.
 */
export async function GET(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const scoping = resolveBankScoping(new URL(request.url).searchParams);
  if (!scoping) {
    return errorResponse("errors.requiredBankId", 400, locale);
  }
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const response = await proxyUpstream({
    config,
    request,
    upstreamPath: `/v1/default/memories/${encodeURIComponent((await context.params).memoryId)}/history`,
    query: searchParamsObject(request),
    method: "GET",
  });
  if (response.status === 404) {
    return errorResponse("errors.notFound", 404, locale);
  }
  if (!response.ok) {
    return errorResponse("errors.internal", 500, locale);
  }
  return response;
}