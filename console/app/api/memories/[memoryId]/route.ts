import { resolveServerConfig } from "../../../../lib/server/config";
import {
  requestLocale,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../../lib/server/proxy";
import { errorResponse } from "../../../../lib/server/http";
import { callUpstream } from "../../../../lib/server/upstream";

type Context = { params: Promise<{ memoryId: string }> };

const CURATION_FIELDS = [
  "text",
  "context",
  "occurred_start",
  "occurred_end",
  "fact_type",
  "entities",
  "resolve_entities",
  "state",
  "reason",
] as const;

function memoryPath(memoryId: string): string {
  return `/v1/default/memories/${encodeURIComponent(memoryId)}`;
}

function readBankId(request: Request, body?: Record<string, unknown>): string | undefined {
  const fromBody = body?.bank_id;
  if (typeof fromBody === "string" && fromBody.length > 0) return fromBody;
  const fromQuery = new URL(request.url).searchParams.get("bank_id");
  return fromQuery && fromQuery.length > 0 ? fromQuery : undefined;
}

/**
 * Per-memory read.
 *
 * The bank id is required. An upstream 404 becomes a fixed 404 carrying the
 * memory not-found key; any other upstream failure becomes a 500 with the
 * memory-fetch key rather than passing the upstream status through.
 */
export async function GET(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const bankId = readBankId(request);
  if (!bankId) return errorResponse("api.errors.validation.bankIdRequired", 400, locale);

  const memoryId = (await context.params).memoryId;
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);

  let result;
  try {
    result = await callUpstream(config, { path: memoryPath(memoryId), method: "GET" });
  } catch {
    return errorResponse("api.errors.memories.fetch", 500, locale);
  }

  if (result.status === 404) return errorResponse("api.errors.memories.notFound", 404, locale);
  if (!result.ok) return errorResponse("api.errors.memories.fetch", 500, locale);

  const headers = new Headers();
  if (result.contentType) headers.set("content-type", result.contentType);
  return new Response(result.body, { status: 200, headers });
}

/**
 * Per-memory update.
 *
 * The bank id comes from the JSON body, falling back to the bank_id query
 * parameter, and is excluded from the body forwarded upstream because it is a
 * routing value rather than a curation field. An unparseable body is a 400.
 */
export async function PATCH(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const raw = await request.text();

  let parsed: Record<string, unknown>;
  try {
    parsed = JSON.parse(raw) as Record<string, unknown>;
  } catch {
    return errorResponse("api.errors.auth.invalidRequestBody", 400, locale);
  }

  const bankId = readBankId(request, parsed);
  if (!bankId) return errorResponse("api.errors.validation.bankIdRequired", 400, locale);

  const forwarded: Record<string, unknown> = {};
  for (const field of CURATION_FIELDS) {
    if (parsed[field] !== undefined) forwarded[field] = parsed[field];
  }

  const memoryId = (await context.params).memoryId;
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const failure = {
    failureLabel: "memories_update_failed",
    errorKey: "api.errors.memories.update",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: memoryPath(memoryId),
      method: "PATCH",
      body: JSON.stringify(forwarded),
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}
