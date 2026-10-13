import { resolveServerConfig } from "../../../lib/server/config";
import {
  bankScopedPath,
  requestLocale,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../lib/server/proxy";
import { errorResponse, passthroughQueryParams, tagFilterParams } from "../../../lib/server/http";

const GRAPH_PARAMS = ["limit", "min_count"] as const;

/**
 * Graph retrieval for the visualization views.
 *
 * The bank id is required. Returns 200 with nodes, edges and the totals and
 * limit the graph was built with.
 */
export async function GET(request: Request): Promise<Response> {
  const locale = requestLocale(request);
  const searchParams = new URL(request.url).searchParams;
  const bankId = searchParams.get("bank_id") ?? searchParams.get("agent_id");
  if (!bankId) return errorResponse("api.errors.validation.bankIdRequired", 400, locale);

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const failure = { failureLabel: "graph_fetch_failed", errorKey: "api.errors.graph.fetch", locale };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: bankScopedPath("/v1/default/banks", bankId, "/graph"),
      query: { ...tagFilterParams(request), ...passthroughQueryParams(request, GRAPH_PARAMS) },
      method: "GET",
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}
