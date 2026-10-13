import { resolveServerConfig } from "../../../../lib/server/config";
import { proxyUpstream, requestLocale, searchParamsObject } from "../../../../lib/server/proxy";
import { errorResponse, requiredQueryParam } from "../../../../lib/server/http";

/**
 * Document reprocess.
 *
 * Bank and document identifiers travel in the query string by contract.
 */
export async function POST(request: Request): Promise<Response> {
  const locale = requestLocale(request);
  const searchParams = new URL(request.url).searchParams;
  const documentId = requiredQueryParam(searchParams, "document_id");
  if (!documentId) {
    return errorResponse("errors.requiredDocumentId", 400, locale);
  }
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const body = await request.text();
  return proxyUpstream({
    config,
    request,
    upstreamPath: "/v1/default/documents/reprocess",
    query: searchParamsObject(request),
    method: "POST",
    body: body.length > 0 ? body : undefined,
  });
}