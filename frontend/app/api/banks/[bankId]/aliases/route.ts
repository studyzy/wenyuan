import { resolveServerConfig } from "../../../../../lib/server/config";
import {
  bankScopedPath,
  requestLocale,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../../../lib/server/proxy";
import { errorResponse } from "../../../../../lib/server/http";

type Context = { params: Promise<{ bankId: string }> };

/**
 * Bank aliases sub-resource.
 *
 * GET returns 200 with the upstream alias payload. POST adds an alias from the
 * caller's JSON body, forwarded verbatim, and returns 201.
 */
export async function GET(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const bankId = (await context.params).bankId;
  if (!bankId) return errorResponse("api.errors.validation.bankIdRequired", 400, locale);

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const failure = { failureLabel: "aliases_list_failed", errorKey: "api.errors.bankAliases.list", locale };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: bankScopedPath("/v1/default/banks", bankId, "/aliases"),
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
  const failure = { failureLabel: "aliases_create_failed", errorKey: "api.errors.bankAliases.create", locale };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: bankScopedPath("/v1/default/banks", bankId, "/aliases"),
      method: "POST",
      body,
      successStatus: 201,
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}
