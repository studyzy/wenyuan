import { resolveServerConfig } from "../../../../../../lib/server/config";
import {
  bankScopedPath,
  requestLocale,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../../../../lib/server/proxy";
import { errorResponse, passthroughQueryParams } from "../../../../../../lib/server/http";

type Context = { params: Promise<{ bankId: string }> };

const SCOPES_PARAMS = ["limit", "offset"] as const;

/**
 * Observation-scope listing.
 *
 * Returns 200 with a scopes list of tag/count pairs plus total, limit and
 * offset.
 */
export async function GET(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const bankId = (await context.params).bankId;
  if (!bankId) return errorResponse("api.errors.validation.bankIdRequired", 400, locale);

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const failure = {
    failureLabel: "observation_scopes_failed",
    errorKey: "api.errors.observations.list",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: bankScopedPath("/v1/default/banks", bankId, "/observations/scopes"),
      query: passthroughQueryParams(request, SCOPES_PARAMS),
      method: "GET",
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}
