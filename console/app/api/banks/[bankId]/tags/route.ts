import { resolveServerConfig } from "../../../../../lib/server/config";
import {
  bankScopedPath,
  requestLocale,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../../../lib/server/proxy";
import { errorResponse, passthroughQueryParams } from "../../../../../lib/server/http";

type Context = { params: Promise<{ bankId: string }> };

const LIST_PARAMS = ["limit", "offset"] as const;

/**
 * Bank tag listing.
 *
 * Returns 200 with an items list of tag/count pairs plus total, limit and
 * offset.
 */
export async function GET(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const bankId = (await context.params).bankId;
  if (!bankId) return errorResponse("api.errors.validation.bankIdRequired", 400, locale);

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const failure = {
    failureLabel: "tags_list_failed",
    errorKey: "api.errors.tags.list",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: bankScopedPath("/v1/default/banks", bankId, "/tags"),
      query: passthroughQueryParams(request, LIST_PARAMS),
      method: "GET",
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}
