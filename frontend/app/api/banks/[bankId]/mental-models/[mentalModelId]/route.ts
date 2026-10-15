import { resolveServerConfig } from "../../../../../../lib/server/config";
import {
  bankScopedPath,
  requestLocale,
  serializeSynthesizedDelete,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../../../../lib/server/proxy";
import { errorResponse } from "../../../../../../lib/server/http";

type Context = { params: Promise<{ bankId: string; mentalModelId: string }> };

function modelPath(bankId: string, mentalModelId: string): string {
  return bankScopedPath(
    "/v1/default/banks",
    bankId,
    `/mental-models/${encodeURIComponent(mentalModelId)}`,
  );
}

/**
 * Per-mental-model sub-resource: GET reads, PATCH updates, DELETE removes.
 *
 * Read and update return 200 with the model. Delete returns 200 with a
 * synthesized success payload rather than the upstream delete body, which is
 * observable and differs from the other bank proxy routes.
 */
export async function GET(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const { bankId, mentalModelId } = await context.params;
  if (!bankId || !mentalModelId) {
    return errorResponse("api.errors.validation.bankAndMentalModelIdRequired", 400, locale);
  }

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const failure = {
    failureLabel: "mental_model_fetch_failed",
    errorKey: "api.errors.mentalModels.fetch",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: modelPath(bankId, mentalModelId),
      method: "GET",
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}

export async function PATCH(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const { bankId, mentalModelId } = await context.params;
  if (!bankId || !mentalModelId) {
    return errorResponse("api.errors.validation.bankAndMentalModelIdRequired", 400, locale);
  }

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const body = await request.text();
  const failure = {
    failureLabel: "mental_model_update_failed",
    errorKey: "api.errors.mentalModels.update",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: modelPath(bankId, mentalModelId),
      method: "PATCH",
      body,
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}

export async function DELETE(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const { bankId, mentalModelId } = await context.params;
  if (!bankId || !mentalModelId) {
    return errorResponse("api.errors.validation.bankAndMentalModelIdRequired", 400, locale);
  }

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const failure = {
    failureLabel: "mental_model_delete_failed",
    errorKey: "api.errors.mentalModels.delete",
    locale,
  };

  try {
    return await serializeSynthesizedDelete({
      config,
      request,
      upstreamPath: modelPath(bankId, mentalModelId),
      method: "DELETE",
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}
