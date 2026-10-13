import { resolveServerConfig } from "../../../lib/server/config";
import { proxyUpstream, searchParamsObject } from "../../../lib/server/proxy";
import { errorResponse, requiredQueryParam } from "../../../lib/server/http";
import { requestLocale } from "../../../lib/server/proxy";

/**
 * Document collection.
 *
 * Document ids travel in the query string by contract, never as a path segment,
 * because intermediaries normalize encoded slashes.
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
    upstreamPath: "/v1/default/documents",
    query: searchParamsObject(request),
    method: "GET",
  });
}

export async function PATCH(request: Request): Promise<Response> {
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
    upstreamPath: "/v1/default/documents",
    query: searchParamsObject(request),
    method: "PATCH",
    body,
  });
}

export async function DELETE(request: Request): Promise<Response> {
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
    upstreamPath: "/v1/default/documents",
    query: searchParamsObject(request),
    method: "DELETE",
  });
}