import { resolveServerConfig } from "../../../../../lib/server/config";
import {
  bankScopedPath,
  requestLocale,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../../../lib/server/proxy";
import {
  errorResponse,
  passthroughQueryParams,
  tagFilterParams,
} from "../../../../../lib/server/http";

type Context = { params: Promise<{ bankId: string }> };

const LIST_PARAMS = ["detail", "limit", "offset"] as const;

function scopedQuery(request: Request): Record<string, string | string[]> {
  return { ...tagFilterParams(request), ...passthroughQueryParams(request, LIST_PARAMS) };
}

/**
 * Mental-model collection: GET lists with pagination, a detail selector and an
 * optional tag filter; POST creates.
 *
 * Create returns 202 with an operation id because the model content is
 * generated in the background rather than at request time.
 */
export async function GET(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const bankId = (await context.params).bankId;
  if (!bankId) return errorResponse("api.errors.validation.bankIdRequired", 400, locale);

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const failure = {
    failureLabel: "mental_models_list_failed",
    errorKey: "api.errors.mentalModels.list",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: bankScopedPath("/v1/default/banks", bankId, "/mental-models"),
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
    failureLabel: "mental_models_create_failed",
    errorKey: "api.errors.mentalModels.create",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: bankScopedPath("/v1/default/banks", bankId, "/mental-models"),
      method: "POST",
      body,
      successStatus: 202,
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}