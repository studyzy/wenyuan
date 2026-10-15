import { resolveServerConfig } from "../../../../lib/server/config";
import {
  bankScopedPath,
  requestLocale,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../../lib/server/proxy";
import { errorResponse } from "../../../../lib/server/http";

/**
 * Asynchronous retain.
 *
 * The route name spelling retains the underscore, which is fixed compatibility
 * surface. The bank id comes from the JSON body or the bank_id query parameter.
 * An unparseable body is a 400.
 */
export async function POST(request: Request): Promise<Response> {
  const locale = requestLocale(request);
  const raw = await request.text();

  let bankId: string | undefined;
  try {
    const parsed = JSON.parse(raw) as Record<string, unknown>;
    const fromBody = parsed?.bank_id;
    if (typeof fromBody === "string" && fromBody.length > 0) bankId = fromBody;
  } catch {
    return errorResponse("api.errors.auth.invalidRequestBody", 400, locale);
  }
  if (!bankId) bankId = new URL(request.url).searchParams.get("bank_id") ?? undefined;
  if (!bankId) return errorResponse("api.errors.validation.bankIdRequired", 400, locale);

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const failure = {
    failureLabel: "memories_retain_async_failed",
    errorKey: "api.errors.memories.retain",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: bankScopedPath("/v1/default/banks", bankId, "/memories"),
      method: "POST",
      body: raw,
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}
