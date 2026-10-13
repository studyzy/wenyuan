import { resolveServerConfig } from "../../../../../../lib/server/config";
import {
  bankScopedPath,
  requestLocale,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../../../../lib/server/proxy";
import { booleanQueryFlags, errorResponse } from "../../../../../../lib/server/http";

type Context = { params: Promise<{ bankId: string; operationId: string }> };

function operationPath(bankId: string, operationId: string): string {
  return bankScopedPath(
    "/v1/default/banks",
    bankId,
    `/operations/${encodeURIComponent(operationId)}`,
  );
}

/**
 * Per-operation lookup, retry and delete.
 *
 * GET returns 200 with the operation status fields, accepting an
 * include_payload flag. POST retries the operation. DELETE removes it. Each
 * returns the upstream payload at 200.
 */
export async function GET(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const { bankId, operationId } = await context.params;
  if (!bankId || !operationId) {
    return errorResponse("api.errors.validation.operationIdRequired", 400, locale);
  }

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const failure = {
    failureLabel: "operations_status_failed",
    errorKey: "api.errors.operations.status",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: operationPath(bankId, operationId),
      query: booleanQueryFlags(request, ["include_payload"]),
      method: "GET",
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}

export async function POST(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const { bankId, operationId } = await context.params;
  if (!bankId || !operationId) {
    return errorResponse("api.errors.validation.operationIdRequired", 400, locale);
  }

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const body = await request.text();
  const failure = {
    failureLabel: "operations_retry_failed",
    errorKey: "api.errors.operations.retry",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: operationPath(bankId, operationId),
      method: "POST",
      body: body.length > 0 ? body : undefined,
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}

export async function DELETE(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const { bankId, operationId } = await context.params;
  if (!bankId || !operationId) {
    return errorResponse("api.errors.validation.operationIdRequired", 400, locale);
  }

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const failure = {
    failureLabel: "operations_delete_failed",
    errorKey: "api.errors.operations.delete",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: operationPath(bankId, operationId),
      method: "DELETE",
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}
