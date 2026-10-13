import { resolveServerConfig } from "../../../../../lib/server/config";
import {
  bankScopedPath,
  requestLocale,
  searchParamsObject,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../../../lib/server/proxy";
import { errorResponse } from "../../../../../lib/server/http";

type Context = { params: Promise<{ bankId: string }> };

/**
 * Bank LLM-request log listing.
 *
 * Returns 200 with a bank id, total, limit, offset and an items list. Any
 * query parameters are forwarded to the upstream verbatim.
 */
export async function GET(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const bankId = (await context.params).bankId;
  if (!bankId) return errorResponse("api.errors.validation.bankIdRequired", 400, locale);

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);

  const failure = { failureLabel: "llm_requests_list_failed", errorKey: "api.errors.llmRequests.list", locale };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: bankScopedPath("/v1/default/banks", bankId, "/llm-requests"),
      query: searchParamsObject(request),
      method: "GET",
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}