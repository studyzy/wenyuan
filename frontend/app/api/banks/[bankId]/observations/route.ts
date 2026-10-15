import { resolveServerConfig } from "../../../../../lib/server/config";
import { bankScopedPath, requestLocale } from "../../../../../lib/server/proxy";
import { errorResponse, jsonResponse } from "../../../../../lib/server/http";
import { callUpstream } from "../../../../../lib/server/upstream";

type Context = { params: Promise<{ bankId: string }> };

type MemoryItem = {
  id?: string;
  text?: string;
  proof_count?: number;
  tags?: string[];
  date?: string;
};

/**
 * Observations collection.
 *
 * GET does not proxy an observations endpoint: it lists memories pinned to the
 * observation type with a fixed limit of one thousand, then reshapes each item
 * into the observations shape and returns them under an items object. No tag
 * filter is supported here even though the sibling list routes accept one. Any
 * upstream failure becomes a 500 with the observations-list key rather than a
 * status passthrough.
 *
 * DELETE returns 200 with the clear payload.
 */
export async function GET(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const bankId = (await context.params).bankId;
  if (!bankId) return errorResponse("api.errors.validation.bankIdRequired", 400, locale);

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);

  let result;
  try {
    result = await callUpstream(config, {
      path: bankScopedPath("/v1/default/banks", bankId, "/memories/list"),
      method: "GET",
      query: { type: "observation", limit: "1000" },
    });
  } catch {
    return errorResponse("api.errors.observations.list", 500, locale);
  }

  if (!result.ok) {
    return errorResponse("api.errors.observations.list", 500, locale);
  }

  let payload: { items?: MemoryItem[] } = {};
  try {
    payload = JSON.parse(new TextDecoder().decode(result.body)) as { items?: MemoryItem[] };
  } catch {
    return errorResponse("api.errors.observations.list", 500, locale);
  }

  const items = (payload.items ?? []).map((item) => ({
    id: item.id,
    bank_id: bankId,
    text: item.text,
    proof_count: item.proof_count ?? 1,
    history: [],
    tags: item.tags ?? [],
    source_memory_ids: [],
    source_memories: [],
    created_at: item.date,
    updated_at: item.date,
  }));

  return jsonResponse({ items });
}

export async function DELETE(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const bankId = (await context.params).bankId;
  if (!bankId) return errorResponse("api.errors.validation.bankIdRequired", 400, locale);

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);

  let result;
  try {
    result = await callUpstream(config, {
      path: bankScopedPath("/v1/default/banks", bankId, "/observations"),
      method: "DELETE",
    });
  } catch {
    return errorResponse("api.errors.observations.clear", 500, locale);
  }

  if (!result.ok) {
    return errorResponse("api.errors.observations.clear", 500, locale);
  }

  const headers = new Headers({ "content-type": "application/json; charset=utf-8" });
  return new Response(JSON.stringify({ success: true }), { status: 200, headers });
}
