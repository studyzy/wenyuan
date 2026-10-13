import { resolveServerConfig } from "../../../../lib/server/config";
import { proxyUpstream, requestLocale, searchParamsObject } from "../../../../lib/server/proxy";
import { errorResponse, requiredQueryParam } from "../../../../lib/server/http";

/**
 * Document chunk listing.
 *
 * Bank and document identifiers travel in the query string by contract.
 */
export async function GET(request: Request): Promise<Response> {
  const locale = requestLocale(request);
  const searchParams = new URL(request.url).searchParams;
  const documentId = requiredQueryParam(searchParams, "document_id");
  if (!documentId) {
    return errorResponse("errors.requiredDocumentId", 400, locale);
  }
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  return proxyUpstream({
    config,
    request,
    upstreamPath: "/v1/default/documents/chunks",
    query: searchParamsObject(request),
    method: "GET",
  });
}