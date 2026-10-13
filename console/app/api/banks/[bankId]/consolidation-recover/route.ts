import { resolveServerConfig } from "../../../../../lib/server/config";
import { bankScopedPath, requestLocale, serializeUpstream, upstreamUnavailableBody } from "../../../../../lib/server/proxy";
import { errorResponse } from "../../../../../lib/server/http";

type Context = { params: Promise<{ bankId: string }> };

/**
 * Bank consolidation recovery trigger.
 *
 * Resets memories previously marked as consolidation-failed so they are
 * retried, returning 200 with the retried count.
 */
export async function POST(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const body = await request.text();

  const failure = { failureLabel: "consolidation_recover_failed", errorKey: "api.errors.generic.internal", locale };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: bankScopedPath(
        "/v1/default/banks",
        (await context.params).bankId,
        "/consolidation-recover",
      ),
      method: "POST",
      body: body.length > 0 ? body : undefined,
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}