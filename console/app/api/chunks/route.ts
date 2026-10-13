import { resolveServerConfig } from "../../../lib/server/config";
import { proxyUpstream, requestLocale, searchParamsObject } from "../../../lib/server/proxy";
import { errorResponse, requiredQueryParam } from "../../../lib/server/http";

/** Chunk lookup keyed by the chunk_id query parameter. */
export async function GET(request: Request): Promise<Response> {
  const locale = requestLocale(request);
  const searchParams = new URL(request.url).searchParams;
  const chunkId = requiredQueryParam(searchParams, "chunk_id");
  if (!chunkId) {
    return errorResponse("errors.requiredChunkId", 400, locale);
  }
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  return proxyUpstream({
    config,
    request,
    upstreamPath: "/v1/default/chunks",
    query: searchParamsObject(request),
    method: "GET",
  });
}