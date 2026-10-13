import { resolveServerConfig } from "../../../../../../lib/server/config";
import {
  bankScopedPath,
  requestLocale,
  searchParamsObject,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../../../../lib/server/proxy";
import { errorResponse } from "../../../../../../lib/server/http";

type Context = { params: Promise<{ bankId: string }> };

/**
 * Bank LLM-request statistics.
 *
 * Returns 200 with a bank id, period, truncation marker, start and a buckets
 * list. Any query parameters are forwarded to the upstream verbatim.
 */
export async function GET(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const bankId = (await context.params).bankId;
  if (!bankId) return errorResponse("api.errors.validation.bankIdRequired", 400, locale);

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);

  const failure = { failureLabel: "llm_requests_stats_failed", errorKey: "api.errors.llmRequests.stats", locale };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: bankScopedPath("/v1/default/banks", bankId, "/llm-requests/stats"),
      query: searchParamsObject(request),
      method: "GET",
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}