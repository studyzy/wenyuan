import { resolveServerConfig } from "../../../lib/server/config";
import { proxyUpstream, requestLocale, searchParamsObject } from "../../../lib/server/proxy";
import { errorResponse, resolveBankScoping } from "../../../lib/server/http";

/** Entity listing. Bank scoping accepts the legacy agent-style alias. */
export async function GET(request: Request): Promise<Response> {
  const locale = requestLocale(request);
  const scoping = resolveBankScoping(new URL(request.url).searchParams);
  if (!scoping) {
    return errorResponse("errors.requiredBankId", 400, locale);
  }
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  return proxyUpstream({
    config,
    request,
    upstreamPath: "/v1/default/entities",
    query: searchParamsObject(request),
    method: "GET",
  });
}