import { resolveServerConfig } from "../../../lib/server/config";
import { bankScopedPath, requestLocale } from "../../../lib/server/proxy";
import { errorResponse } from "../../../lib/server/http";
import { callUpstream } from "../../../lib/server/upstream";

/**
 * Recall.
 *
 * The bank the memories are recalled from travels in the request body, because
 * that is the shape the browser client posts; a request that names no bank falls
 * back to the default one. `fact_type` is the legacy spelling of `types` and is
 * normalised rather than forwarded, and the budget defaults so the retrieval
 * arms always have one. Returns the whole upstream body unchanged at 200 with no
 * field projection — a hand-picked projection dropped fields the caller asked
 * for. A failure, or a success that carries no data, is a 500 with the recall
 * key.
 */
export async function POST(request: Request): Promise<Response> {
  const locale = requestLocale(request);

  let body: Record<string, unknown>;
  try {
    body = (await request.json()) as Record<string, unknown>;
  } catch {
    return errorResponse("api.errors.recall.failed", 500, locale);
  }

  const bankId = (body.bank_id as string) || (body.agent_id as string) || "default";

  const upstreamBody: Record<string, unknown> = {
    query: body.query,
    types: body.types ?? body.fact_type,
    prefer_observations: body.prefer_observations,
    max_tokens: body.max_tokens,
    trace: body.trace,
    budget: body.budget || "mid",
    include: body.include,
    query_timestamp: body.query_timestamp,
    tags: body.tags,
    tags_match: body.tags_match,
    tag_groups: body.tag_groups,
    min_scores: body.min_scores,
    temporal_window: body.temporal_window,
  };

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);

  let result;
  try {
    result = await callUpstream(config, {
      path: bankScopedPath("/v1/default/banks", bankId, "/memories/recall"),
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(upstreamBody),
    });
  } catch {
    return errorResponse("api.errors.recall.failed", 500, locale);
  }

  if (!result.ok || result.body.byteLength === 0) {
    return errorResponse("api.errors.recall.failed", 500, locale);
  }

  const headers = new Headers({
    "content-type": result.contentType ?? "application/json; charset=utf-8",
  });
  return new Response(result.body, { status: 200, headers });
}