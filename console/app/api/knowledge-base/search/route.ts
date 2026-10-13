import { resolveServerConfig } from "../../../../lib/server/config";
import {
  bankScopedPath,
  requestLocale,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../../lib/server/proxy";
import { errorResponse, jsonResponse } from "../../../../lib/server/http";

/**
 * Knowledge-base search.
 *
 * The bank id is required. The query term defaults the limit to ten. A search
 * with no query term short-circuits to an empty result set without contacting
 * the upstream.
 */
export async function GET(request: Request): Promise<Response> {
  const locale = requestLocale(request);
  const searchParams = new URL(request.url).searchParams;
  const bankId = searchParams.get("bank_id");
  if (!bankId) return errorResponse("api.errors.validation.bankIdRequired", 400, locale);

  const query = searchParams.get("q");
  if (!query) return jsonResponse({ results: [], total: 0 });

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const failure = {
    failureLabel: "knowledge_base_search_failed",
    errorKey: "api.errors.knowledgeBase.search",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: bankScopedPath("/v1/default/banks", bankId, "/knowledge-base/search"),
      query: { q: query, limit: searchParams.get("limit") ?? "10" },
      method: "GET",
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}
