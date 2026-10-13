import { resolveServerConfig } from "../../../lib/server/config";
import { bankScopedPath, requestLocale } from "../../../lib/server/proxy";
import { errorResponse } from "../../../lib/server/http";
import { callUpstream } from "../../../lib/server/upstream";

/**
 * Recall.
 *
 * The bank id is required. Returns the whole upstream recall body unchanged at
 * 200 with no field projection. A failure, or a success that carries no data,
 * is a 500 with the recall key.
 */
export async function POST(request: Request): Promise<Response> {
  const locale = requestLocale(request);
  const searchParams = new URL(request.url).searchParams;
  const bankId = searchParams.get("bank_id") ?? searchParams.get("agent_id");
  if (!bankId) return errorResponse("api.errors.validation.bankIdRequired", 400, locale);

  const body = await request.text();
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);

  let result;
  try {
    result = await callUpstream(config, {
      path: bankScopedPath("/v1/default/banks", bankId, "/memories/recall"),
      method: "POST",
      headers: { "content-type": "application/json" },
      body,
    });
  } catch {
    return errorResponse("api.errors.recall.failed", 500, locale);
  }

  if (!result.ok || result.body.byteLength === 0) {
    return errorResponse("api.errors.recall.failed", 500, locale);
  }

  const headers = new Headers({
    "content-type": result.contentType ?? "application/json; charset=utf-8",
  });
  return new Response(result.body, { status: 200, headers });
}
