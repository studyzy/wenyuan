import { resolveServerConfig } from "../../../../lib/server/config";
import {
  bankScopedPath,
  requestLocale,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../../lib/server/proxy";
import { errorResponse } from "../../../../lib/server/http";

type Context = { params: Promise<{ bankId: string }> };

function guard(bankId: string | undefined, locale: string): Response | undefined {
  if (!bankId) return errorResponse("api.errors.validation.bankIdRequired", 400, locale);
  return undefined;
}

/**
 * Bank-scoped lifecycle: PUT and PATCH update from a JSON body, DELETE removes.
 * All three return 200 with the upstream payload.
 */
export async function PUT(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const bankId = (await context.params).bankId;
  const denied = guard(bankId, locale);
  if (denied) return denied;

  const raw = await request.text();
  try {
    JSON.parse(raw);
  } catch {
    return errorResponse("api.errors.auth.invalidRequestBody", 400, locale);
  }

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const failure = { failureLabel: "banks_update_failed", errorKey: "api.errors.banks.update", locale };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: bankScopedPath("/v1/default/banks", bankId),
      method: "PUT",
      body: raw,
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}

export async function PATCH(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const bankId = (await context.params).bankId;
  const denied = guard(bankId, locale);
  if (denied) return denied;

  const raw = await request.text();
  try {
    JSON.parse(raw);
  } catch {
    return errorResponse("api.errors.auth.invalidRequestBody", 400, locale);
  }

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const failure = { failureLabel: "banks_update_failed", errorKey: "api.errors.banks.update", locale };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: bankScopedPath("/v1/default/banks", bankId),
      method: "PATCH",
      body: raw,
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}

export async function DELETE(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const bankId = (await context.params).bankId;
  const denied = guard(bankId, locale);
  if (denied) return denied;

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const failure = { failureLabel: "banks_delete_failed", errorKey: "api.errors.banks.delete", locale };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: bankScopedPath("/v1/default/banks", bankId),
      method: "DELETE",
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}
