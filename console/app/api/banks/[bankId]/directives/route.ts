import { resolveServerConfig } from "../../../../../lib/server/config";
import {
  bankScopedPath,
  requestLocale,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../../../lib/server/proxy";
import { errorResponse, passthroughQueryParams, tagFilterParams } from "../../../../../lib/server/http";

type Context = { params: Promise<{ bankId: string }> };

const LIST_PARAMS = ["limit", "offset"] as const;

function scopedQuery(request: Request): Record<string, string | string[]> {
  return { ...tagFilterParams(request), ...passthroughQueryParams(request, LIST_PARAMS) };
}

/**
 * Directive collection: GET lists with pagination and an optional tag filter,
 * POST creates.
 *
 * The list returns 200 with an items list plus total, limit and offset. Create
 * returns 201 with the created directive and forwards the caller's body
 * verbatim.
 */
export async function GET(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const bankId = (await context.params).bankId;
  if (!bankId) return errorResponse("api.errors.validation.bankIdRequired", 400, locale);

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const failure = {
    failureLabel: "directives_list_failed",
    errorKey: "api.errors.directives.list",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: bankScopedPath("/v1/default/banks", bankId, "/directives"),
      query: scopedQuery(request),
      method: "GET",
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}

export async function POST(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const bankId = (await context.params).bankId;
  if (!bankId) return errorResponse("api.errors.validation.bankIdRequired", 400, locale);

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const body = await request.text();
  const failure = {
    failureLabel: "directives_create_failed",
    errorKey: "api.errors.directives.create",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: bankScopedPath("/v1/default/banks", bankId, "/directives"),
      method: "POST",
      body,
      successStatus: 201,
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}