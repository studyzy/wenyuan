import { resolveServerConfig } from "../../../../../../../lib/server/config";
import { bankScopedPath, requestLocale } from "../../../../../../../lib/server/proxy";
import { errorResponse } from "../../../../../../../lib/server/http";
import { callUpstream } from "../../../../../../../lib/server/upstream";

type Context = { params: Promise<{ bankId: string; mentalModelId: string }> };

/**
 * Mental-model history child.
 *
 * An upstream 404 is translated into a localized not-found payload; any other
 * upstream failure surfaces as a localized 500 rather than an unhandled error.
 */
export async function GET(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const { bankId, mentalModelId } = await context.params;

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const url = new URL(request.url);
  const result = await callUpstream(config, {
    path: bankScopedPath(
      "/v1/default/banks",
      bankId,
      `/mental-models/${encodeURIComponent(mentalModelId)}/history`,
    ),
    method: "GET",
    query: Object.fromEntries(url.searchParams.entries()),
  });

  if (result.status === 404) {
    return errorResponse("errors.notFound", 404, locale);
  }
  if (!result.ok) {
    return errorResponse("errors.internal", 500, locale);
  }

  const headers = new Headers();
  if (result.contentType) headers.set("content-type", result.contentType);
  return new Response(result.body, { status: result.status, headers });
}