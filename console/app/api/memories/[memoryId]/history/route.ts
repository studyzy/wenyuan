import { resolveServerConfig } from "../../../../../lib/server/config";
import { bankScopedPath, requestLocale } from "../../../../../lib/server/proxy";
import { errorResponse } from "../../../../../lib/server/http";
import { callUpstream } from "../../../../../lib/server/upstream";

type Context = { params: Promise<{ memoryId: string }> };

/**
 * Per-memory history child.
 *
 * The bank id is required. Returns 200 with the version entries including
 * resolved source facts. An upstream 404 becomes a fixed 404 carrying the
 * memory not-found key; any other upstream failure becomes a 500 with the
 * history key rather than passing the upstream status through.
 */
export async function GET(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const searchParams = new URL(request.url).searchParams;
  const bankId = searchParams.get("bank_id");
  if (!bankId) return errorResponse("api.errors.validation.bankIdRequired", 400, locale);

  const memoryId = (await context.params).memoryId;
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);

  let result;
  try {
    result = await callUpstream(config, {
      path: bankScopedPath(
        "/v1/default/banks",
        bankId,
        `/memories/${encodeURIComponent(memoryId)}/history`,
      ),
      method: "GET",
    });
  } catch {
    return errorResponse("api.errors.memories.history", 500, locale);
  }

  if (result.status === 404) return errorResponse("api.errors.memories.notFound", 404, locale);
  if (!result.ok) return errorResponse("api.errors.memories.history", 500, locale);

  const headers = new Headers();
  if (result.contentType) headers.set("content-type", result.contentType);
  return new Response(result.body, { status: 200, headers });
}
