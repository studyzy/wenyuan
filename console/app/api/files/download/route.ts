import { resolveServerConfig } from "../../../../lib/server/config";
import { proxyUpstream, requestLocale, searchParamsObject } from "../../../../lib/server/proxy";
import { errorResponse } from "../../../../lib/server/http";
import { guardDownloadPath } from "../../../../lib/server/download";

/**
 * File download proxy.
 *
 * The proxied path is restricted to the upstream file-download prefix; any
 * other path returns 400 without performing an upstream fetch.
 */
export async function GET(request: Request): Promise<Response> {
  const locale = requestLocale(request);
  const searchParams = new URL(request.url).searchParams;
  const requestedPath = searchParams.get("path");

  const guard = guardDownloadPath(requestedPath);
  if (!guard.allowed) {
    return errorResponse("errors.invalidDownloadPath", 400, locale);
  }

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const forwarded = searchParamsObject(request);
  delete forwarded.path;
  return proxyUpstream({
    config,
    request,
    upstreamPath: guard.path,
    query: forwarded,
    method: "GET",
  });
}