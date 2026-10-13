import { resolveServerConfig } from "../../../../../../lib/server/config";
import {
  bankScopedPath,
  requestLocale,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../../../../lib/server/proxy";
import { errorResponse } from "../../../../../../lib/server/http";

type Context = { params: Promise<{ bankId: string; webhookId: string }> };

function webhookPath(bankId: string, webhookId: string): string {
  return bankScopedPath(
    "/v1/default/banks",
    bankId,
    `/webhooks/${encodeURIComponent(webhookId)}`,
  );
}

/**
 * Per-webhook sub-resource: PATCH updates, DELETE removes.
 *
 * Both return the upstream payload without an explicit success status; the
 * caller may send a body or none.
 */
export async function PATCH(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const { bankId, webhookId } = await context.params;
  if (!bankId || !webhookId) {
    return errorResponse("api.errors.validation.bankIdRequired", 400, locale);
  }

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const body = await request.text();
  const failure = {
    failureLabel: "webhooks_update_failed",
    errorKey: "api.errors.webhooks.update",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: webhookPath(bankId, webhookId),
      method: "PATCH",
      body: body.length > 0 ? body : undefined,
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}

export async function DELETE(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const { bankId, webhookId } = await context.params;
  if (!bankId || !webhookId) {
    return errorResponse("api.errors.validation.bankIdRequired", 400, locale);
  }

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const failure = {
    failureLabel: "webhooks_delete_failed",
    errorKey: "api.errors.webhooks.delete",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: webhookPath(bankId, webhookId),
      method: "DELETE",
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}
