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
 * Webhook collection: GET lists, POST creates.
 *
 * The list returns the upstream payload without an explicit success status;
 * create returns 201 with the created webhook and forwards the caller's body
 * verbatim (url, secret, event_types, enabled, http_config).
 */
export async function GET(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const bankId = (await context.params).bankId;
  if (!bankId) return errorResponse("api.errors.validation.bankIdRequired", 400, locale);

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const failure = {
    failureLabel: "webhooks_list_failed",
    errorKey: "api.errors.webhooks.list",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: bankScopedPath("/v1/default/banks", bankId, "/webhooks"),
      query: passthroughQueryParams(request, LIST_PARAMS),
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
    failureLabel: "webhooks_create_failed",
    errorKey: "api.errors.webhooks.create",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: bankScopedPath("/v1/default/banks", bankId, "/webhooks"),
      method: "POST",
      body,
      successStatus: 201,
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}
