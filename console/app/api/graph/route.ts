import { resolveServerConfig } from "../../../lib/server/config";
import {
  bankScopedPath,
  requestLocale,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../lib/server/proxy";
import { errorResponse } from "../../../lib/server/http";

/**
 * Build the graph query from the caller's parameters.
 *
 * `type` accepts its `fact_type` alias. An explicit `tags_match` is forwarded as
 * given — `exact` with no tags selects the global/untagged scope — otherwise tag
 * isolation defaults to `all_strict` whenever tags are present.
 */
function graphQuery(request: Request): Record<string, string | string[]> {
  const searchParams = new URL(request.url).searchParams;
  const query: Record<string, string | string[]> = {};

  const type = searchParams.get("type") || searchParams.get("fact_type");
  if (type) query.type = type;

  const limit = searchParams.get("limit");
  if (limit) query.limit = limit;

  const q = searchParams.get("q");
  if (q) query.q = q;

  const tags = searchParams.getAll("tags").filter((tag) => tag.length > 0);
  if (tags.length > 0) query.tags = tags;

  const tagsMatch = searchParams.get("tags_match");
  if (tagsMatch) query.tags_match = tagsMatch;
  else if (tags.length > 0) query.tags_match = "all_strict";

  const documentId = searchParams.get("document_id");
  if (documentId) query.document_id = documentId;

  const chunkId = searchParams.get("chunk_id");
  if (chunkId) query.chunk_id = chunkId;

  return query;
}

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
      query: graphQuery(request),
      method: "GET",
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}