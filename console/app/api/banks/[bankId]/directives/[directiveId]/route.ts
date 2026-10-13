import { resolveServerConfig } from "../../../../../../lib/server/config";
import {
  bankScopedPath,
  requestLocale,
  serializeSynthesizedDelete,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../../../../lib/server/proxy";
import { errorResponse } from "../../../../../../lib/server/http";

type Context = { params: Promise<{ bankId: string; directiveId: string }> };

function directivePath(bankId: string, directiveId: string): string {
  return bankScopedPath(
    "/v1/default/banks",
    bankId,
    `/directives/${encodeURIComponent(directiveId)}`,
  );
}

/**
 * Per-directive sub-resource: GET reads, PATCH updates, DELETE removes.
 *
 * Read and update return 200 with the directive. Delete returns 200 with a
 * synthesized success payload rather than the upstream delete body, which is
 * observable and differs from the other bank proxy routes.
 */
export async function GET(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const { bankId, directiveId } = await context.params;
  if (!bankId || !directiveId) {
    return errorResponse("api.errors.validation.bankAndDirectiveIdRequired", 400, locale);
  }

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const failure = {
    failureLabel: "directive_fetch_failed",
    errorKey: "api.errors.directives.fetch",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: directivePath(bankId, directiveId),
      method: "GET",
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}

export async function PATCH(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const { bankId, directiveId } = await context.params;
  if (!bankId || !directiveId) {
    return errorResponse("api.errors.validation.bankAndDirectiveIdRequired", 400, locale);
  }

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const body = await request.text();
  const failure = {
    failureLabel: "directive_update_failed",
    errorKey: "api.errors.directives.update",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: directivePath(bankId, directiveId),
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
  const { bankId, directiveId } = await context.params;
  if (!bankId || !directiveId) {
    return errorResponse("api.errors.validation.bankAndDirectiveIdRequired", 400, locale);
  }

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const failure = {
    failureLabel: "directive_delete_failed",
    errorKey: "api.errors.directives.delete",
    locale,
  };

  try {
    return await serializeSynthesizedDelete({
      config,
      request,
      upstreamPath: directivePath(bankId, directiveId),
      method: "DELETE",
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}