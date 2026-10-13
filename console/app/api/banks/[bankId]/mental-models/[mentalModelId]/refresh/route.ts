import { resolveServerConfig } from "../../../../../../../lib/server/config";
import {
  bankScopedPath,
  requestLocale,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../../../../../lib/server/proxy";
import { errorResponse } from "../../../../../../../lib/server/http";

type Context = { params: Promise<{ bankId: string; mentalModelId: string }> };

/**
 * Mental-model refresh child action.
 *
 * Refresh re-runs the source query as an asynchronous operation. Returns 200 with the upstream payload.
 */
export async function POST(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const { bankId, mentalModelId } = await context.params;
  if (!bankId || !mentalModelId) {
    return errorResponse("api.errors.validation.bankAndMentalModelIdRequired", 400, locale);
  }

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const body = await request.text();
  const failure = {
    failureLabel: "mental_model_refresh_failed",
    errorKey: "api.errors.mentalModels.refresh",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: bankScopedPath(
        "/v1/default/banks",
        bankId,
        `/mental-models/${encodeURIComponent(mentalModelId)}/refresh`,
      ),
      method: "POST",
      body: body.length > 0 ? body : undefined,
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}
