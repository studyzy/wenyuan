import { resolveServerConfig } from "../../../../../lib/server/config";
import { proxyUpstream, searchParamsObject } from "../../../../../lib/server/proxy";

type Context = { params: Promise<{ entityId: string }> };

/** Per-entity observation regeneration. */
export async function POST(request: Request, context: Context): Promise<Response> {
  const encoded = (await context.params).entityId;
  let entityId = encoded;
  try {
    entityId = decodeURIComponent(encoded);
  } catch {
    entityId = encoded;
  }
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const body = await request.text();
  return proxyUpstream({
    config,
    request,
    upstreamPath: `/v1/default/entities/${encodeURIComponent(entityId)}/regenerate`,
    query: searchParamsObject(request),
    method: "POST",
    body: body.length > 0 ? body : undefined,
  });
}