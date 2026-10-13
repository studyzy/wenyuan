import { resolveServerConfig } from "../../../lib/server/config";
import {
  requestLocale,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../lib/server/proxy";
import { errorResponse, passthroughQueryParams, tagFilterParams } from "../../../lib/server/http";

const ENTITY_PARAMS = ["limit", "offset"] as const;

function scopedQuery(request: Request): Record<string, string | string[]> {
  return { ...tagFilterParams(request), ...passthroughQueryParams(request, ENTITY_PARAMS) };
}

/**
 * Bank entity collection.
 *
 * The bank id is required. Returns 200 with an items list plus total, limit and
 * offset, and accepts a paginated tag filter.
 */
export async function GET(request: Request): Promise<Response> {
  const locale = requestLocale(request);
  const searchParams = new URL(request.url).searchParams;
  const bankId = searchParams.get("bank_id");
  if (!bankId) return errorResponse("api.errors.validation.bankIdRequired", 400, locale);

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const failure = {
    failureLabel: "entities_list_failed",
    errorKey: "api.errors.entities.list",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: `/v1/default/banks/${encodeURIComponent(bankId)}/entities`,
      query: scopedQuery(request),
      method: "GET",
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}
