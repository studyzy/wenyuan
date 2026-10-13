import { resolveServerConfig } from "../../../../lib/server/config";
import { proxyUpstream, requestLocale, searchParamsObject } from "../../../../lib/server/proxy";
import { errorResponse, resolveBankScoping } from "../../../../lib/server/http";

type Context = { params: Promise<{ entityId: string }> };

/**
 * Per-entity read.
 *
 * The entity id segment is decoded before forwarding so ids containing reserved
 * characters round-trip correctly upstream.
 */
export async function GET(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const scoping = resolveBankScoping(new URL(request.url).searchParams);
  if (!scoping) {
    return errorResponse("errors.requiredBankId", 400, locale);
  }
  const encoded = (await context.params).entityId;
  let entityId = encoded;
  try {
    entityId = decodeURIComponent(encoded);
  } catch {
    entityId = encoded;
  }
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  return proxyUpstream({
    config,
    request,
    upstreamPath: `/v1/default/entities/${encodeURIComponent(entityId)}`,
    query: searchParamsObject(request),
    method: "GET",
  });
}