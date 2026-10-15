import { resolveServerConfig } from "../../../../lib/server/config";
import {
  bankScopedPath,
  requestLocale,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../../lib/server/proxy";
import { booleanQueryFlags, errorResponse, passthroughQueryParams } from "../../../../lib/server/http";

type Context = { params: Promise<{ agentId: string }> };

const QUEUE_PARAMS = ["status", "type", "limit", "offset"] as const;

/**
 * Operation queue keyed by bank id.
 *
 * GET returns 200 with the bank id, total, limit, offset and an operations
 * list, accepting a status and type filter. DELETE cancels an operation by its
 * operation_id query parameter and returns 200 with the cancel payload.
 */
export async function GET(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const bankId = (await context.params).agentId;
  if (!bankId) return errorResponse("api.errors.validation.bankIdRequired", 400, locale);

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const failure = {
    failureLabel: "operations_fetch_failed",
    errorKey: "api.errors.operations.fetch",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: bankScopedPath("/v1/default/banks", bankId, "/operations"),
      query: {
        ...passthroughQueryParams(request, QUEUE_PARAMS),
        ...booleanQueryFlags(request, ["exclude_parents"]),
      },
      method: "GET",
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}

export async function DELETE(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const bankId = (await context.params).agentId;
  if (!bankId) return errorResponse("api.errors.validation.bankIdRequired", 400, locale);

  const operationId = new URL(request.url).searchParams.get("operation_id");
  if (!operationId) {
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
      upstreamPath: bankScopedPath(
        "/v1/default/banks",
        bankId,
        `/operations/${encodeURIComponent(operationId)}`,
      ),
      method: "DELETE",
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}
