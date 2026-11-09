import { resolveServerConfig } from "../../../lib/server/config";
import { bankScopedPath, requestLocale, serializeUpstream } from "../../../lib/server/proxy";
import { errorResponse } from "../../../lib/server/http";

/**
 * Recall.
 *
 * The bank the memories are recalled from travels in the request body, because
 * that is the shape the browser client posts; a request that names no bank falls
 * back to the default one. `fact_type` is the legacy spelling of `types` and is
 * normalised rather than forwarded, and the budget defaults so the retrieval
 * arms always have one. Returns the whole upstream body unchanged at 200 with no
 * field projection — a hand-picked projection dropped fields the caller asked
 * for. An unparseable body is a 500 with the recall key.
 *
 * It goes through the shared serializer rather than calling upstream directly:
 * that is what attaches the tenant the console entered, answers 409 for an
 * operator session with no tenant, and passes the upstream status through
 * instead of reporting every refusal as a server fault.
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

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: bankScopedPath("/v1/default/banks", bankId, "/memories/recall"),
      method: "POST",
      body: JSON.stringify(upstreamBody),
      // The error label stays what this route has always reported, so a client
      // matching on it keeps working.
      failureLabel: "recall_failed",
      errorKey: "api.errors.recall.failed",
      locale,
    });
  } catch {
    return errorResponse("api.errors.recall.failed", 500, locale);
  }
}