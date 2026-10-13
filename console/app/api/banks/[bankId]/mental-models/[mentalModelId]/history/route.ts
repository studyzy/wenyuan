import { resolveServerConfig } from "../../../../../../../lib/server/config";
import { bankScopedPath, requestLocale } from "../../../../../../../lib/server/proxy";
import { errorResponse } from "../../../../../../../lib/server/http";
import { callUpstream } from "../../../../../../../lib/server/upstream";

type Context = { params: Promise<{ bankId: string; mentalModelId: string }> };

/**
 * Mental-model history child.
 *
 * Returns 200 with the version entries. An upstream 404 becomes a fixed 404
 * carrying the mental-model not-found key; any other upstream failure becomes a
 * 500 with the history key rather than passing the upstream status through.
 */
export async function GET(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const { bankId, mentalModelId } = await context.params;
  if (!bankId || !mentalModelId) {
    return errorResponse("api.errors.validation.bankAndMentalModelIdRequired", 400, locale);
  }

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const searchParams = new URL(request.url).searchParams;

  let result;
  try {
    result = await callUpstream(config, {
      path: bankScopedPath(
        "/v1/default/banks",
        bankId,
        `/mental-models/${encodeURIComponent(mentalModelId)}/history`,
      ),
      method: "GET",
      query: Object.fromEntries(searchParams.entries()),
    });
  } catch {
    return errorResponse("api.errors.mentalModels.history", 500, locale);
  }

  if (result.status === 404) {
    return errorResponse("api.errors.mentalModels.notFound", 404, locale);
  }
  if (!result.ok) {
    return errorResponse("api.errors.mentalModels.history", 500, locale);
  }

  const headers = new Headers();
  if (result.contentType) headers.set("content-type", result.contentType);
  return new Response(result.body, { status: 200, headers });
}
