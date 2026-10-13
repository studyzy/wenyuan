import { resolveServerConfig } from "../../../../lib/server/config";
import { proxyUpstream, requestLocale, searchParamsObject } from "../../../../lib/server/proxy";
import { errorResponse, resolveBankScoping } from "../../../../lib/server/http";

/**
 * Multipart file retain for document upload.
 *
 * The configured maximum upload size bounds the proxied request body.
 */
export async function POST(request: Request): Promise<Response> {
  const locale = requestLocale(request);
  const scoping = resolveBankScoping(new URL(request.url).searchParams);
  if (!scoping) {
    return errorResponse("errors.requiredBankId", 400, locale);
  }

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const declared = Number.parseInt(request.headers.get("content-length") ?? "", 10);
  if (Number.isFinite(declared) && declared > config.maxUploadSize) {
    return errorResponse("errors.badRequest", 400, locale);
  }

  const body = await request.arrayBuffer();
  if (body.byteLength > config.maxUploadSize) {
    return errorResponse("errors.badRequest", 400, locale);
  }

  return proxyUpstream({
    config,
    request,
    upstreamPath: "/v1/default/files/retain",
    query: searchParamsObject(request),
    method: "POST",
    body,
  });
}