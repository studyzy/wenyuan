import { resolveServerConfig } from "../../../../../../../lib/server/config";
import {
  bankScopedPath,
  requestLocale,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../../../../../lib/server/proxy";
import { errorResponse } from "../../../../../../../lib/server/http";

type Context = { params: Promise<{ bankId: string; webhookId: string }> };

/**
 * Webhook delivery listing.
 *
 * Returns 200 with an items list and a next cursor. The limit query parameter
 * substitutes a default of the string "50" when the caller omits it.
 */
export async function GET(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const { bankId, webhookId } = await context.params;
  if (!bankId || !webhookId) {
    return errorResponse("api.errors.validation.bankIdRequired", 400, locale);
  }

  const searchParams = new URL(request.url).searchParams;
  const query: Record<string, string> = { limit: searchParams.get("limit") || "50" };
  const cursor = searchParams.get("cursor");
  if (cursor) query.cursor = cursor;

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const failure = {
    failureLabel: "webhook_deliveries_failed",
    errorKey: "api.errors.webhooks.deliveries",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: bankScopedPath(
        "/v1/default/banks",
        bankId,
        `/webhooks/${encodeURIComponent(webhookId)}/deliveries`,
      ),
      query,
      method: "GET",
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}
