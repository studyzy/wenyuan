import { resolveServerConfig } from "../../../../../../lib/server/config";
import { bankScopedPath, requestLocale, serializeUpstream, upstreamUnavailableBody } from "../../../../../../lib/server/proxy";
import { errorResponse } from "../../../../../../lib/server/http";

type Context = { params: Promise<{ bankId: string }> };

/**
 * Consolidation-strategy preview for the strategy editor.
 *
 * Returns 200 with the upstream preview payload: the strategies, the default
 * strategy, the number of scopes scanned and a completion marker.
 */
export async function POST(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const body = await request.text();

  const failure = { failureLabel: "strategy_preview_failed", errorKey: "api.errors.generic.internal", locale };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: bankScopedPath(
        "/v1/default/banks",
        (await context.params).bankId,
        "/consolidation-strategies/preview",
      ),
      method: "POST",
      body,
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}