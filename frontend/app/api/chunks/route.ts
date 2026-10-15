import { resolveServerConfig } from "../../../lib/server/config";
import {
  requestLocale,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../lib/server/proxy";
import { errorResponse, requiredQueryParam } from "../../../lib/server/http";

/**
 * Chunk lookup.
 *
 * The chunk id is required and travels as a path segment on the upstream call.
 * Returns 200 with the chunk payload.
 */
export async function GET(request: Request): Promise<Response> {
  const locale = requestLocale(request);
  const searchParams = new URL(request.url).searchParams;
  const chunkId = requiredQueryParam(searchParams, "chunk_id");
  if (!chunkId) return errorResponse("api.errors.validation.missingRequestData", 400, locale);

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const failure = { failureLabel: "chunks_fetch_failed", errorKey: "api.errors.chunks.fetch", locale };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: `/v1/default/chunks/${encodeURIComponent(chunkId)}`,
      method: "GET",
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}
