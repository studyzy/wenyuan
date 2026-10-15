import { resolveServerConfig } from "../../../../lib/server/config";
import {
  requestLocale,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../../lib/server/proxy";
import { errorResponse, passthroughQueryParams, tagFilterParams } from "../../../../lib/server/http";

const GRAPH_PARAMS = ["limit", "min_count"] as const;

/**
 * Entity graph.
 *
 * The bank id is required. Returns 200 with nodes, edges and the totals and
 * limit the graph was built with. Accepts a limited tag filter.
 */
export async function GET(request: Request): Promise<Response> {
  const locale = requestLocale(request);
  const searchParams = new URL(request.url).searchParams;
  const bankId = searchParams.get("bank_id");
  if (!bankId) return errorResponse("api.errors.validation.bankIdRequired", 400, locale);

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const failure = {
    failureLabel: "entities_graph_failed",
    errorKey: "api.errors.entities.graph",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: `/v1/default/banks/${encodeURIComponent(bankId)}/entities/graph`,
      query: { ...tagFilterParams(request), ...passthroughQueryParams(request, GRAPH_PARAMS) },
      method: "GET",
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}
