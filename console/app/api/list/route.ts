import { resolveServerConfig } from "../../../lib/server/config";
import {
  bankScopedPath,
  requestLocale,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../lib/server/proxy";
import { errorResponse, passthroughQueryParams, tagFilterParams } from "../../../lib/server/http";

const LIST_PARAMS = [
  "limit",
  "offset",
  "type",
  "fact_type",
  "q",
  "consolidation_state",
  "state",
  "document_id",
  "entity_id",
  "time_field",
  "start_date",
  "end_date",
] as const;

/**
 * Memory listing.
 *
 * The bank id is required. Returns 200 with an items list plus total, limit and
 * offset. DELETE never touches storage: individual memory-unit deletion is not
 * supported, so it always answers 501 with the unsupported-delete key.
 */
export async function GET(request: Request): Promise<Response> {
  const locale = requestLocale(request);
  const searchParams = new URL(request.url).searchParams;
  const bankId = searchParams.get("bank_id") ?? searchParams.get("agent_id");
  if (!bankId) return errorResponse("api.errors.validation.bankIdRequired", 400, locale);

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const failure = {
    failureLabel: "memories_list_failed",
    errorKey: "api.errors.memories.list",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: bankScopedPath("/v1/default/banks", bankId, "/memories"),
      query: { ...tagFilterParams(request), ...passthroughQueryParams(request, LIST_PARAMS) },
      method: "GET",
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}

export async function DELETE(request: Request): Promise<Response> {
  const locale = requestLocale(request);
  return errorResponse("api.errors.generic.unsupportedIndividualMemoryDelete", 501, locale);
}
