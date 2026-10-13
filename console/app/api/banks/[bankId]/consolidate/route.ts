import { resolveServerConfig } from "../../../../../lib/server/config";
import { bankScopedPath, requestLocale, serializeUpstream } from "../../../../../lib/server/proxy";
import { errorResponse } from "../../../../../lib/server/http";

type Context = { params: Promise<{ bankId: string }> };

/**
 * Bank consolidation trigger.
 *
 * Returns 200 with the operation id and a deduplicated marker: an in-flight
 * consolidation may be reused rather than started again. A body is forwarded
 * when the caller sent one.
 */
export async function POST(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const body = await request.text();

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: bankScopedPath(
        "/v1/default/banks",
        (await context.params).bankId,
        "/consolidate",
      ),
      method: "POST",
      body: body.length > 0 ? body : undefined,
      failureLabel: "consolidate_failed",
      errorKey: "api.errors.generic.internal",
      locale,
    });
  } catch {
    return errorResponse("api.errors.generic.internal", 500, locale);
  }
}