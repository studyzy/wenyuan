import { resolveServerConfig } from "../../../../../../lib/server/config";
import {
  bankScopedPath,
  requestLocale,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../../../../lib/server/proxy";
import { errorResponse } from "../../../../../../lib/server/http";

type Context = { params: Promise<{ bankId: string }> };

/**
 * Bank-scoped LLM connectivity probe.
 *
 * Returns 200 with the bank id and a list of per-operation probe results
 * (operation, ok, status, latency_ms). Goes through the shared SDK path: a
 * failure with no upstream status at all reports 502 rather than a fabricated
 * code.
 */
export async function POST(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const bankId = (await context.params).bankId;
  if (!bankId) return errorResponse("api.errors.validation.bankIdRequired", 400, locale);

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const failure = {
    failureLabel: "bank_llm_health_failed",
    errorKey: "api.errors.banks.healthLlm",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: bankScopedPath("/v1/default/banks", bankId, "/health/llm"),
      method: "POST",
      ...failure,
    });
  } catch {
    // The shared SDK path reports 502 when the call fails without a status.
    return upstreamUnavailableBody(failure);
  }
}