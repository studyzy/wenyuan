import { resolveServerConfig } from "../../../../lib/server/config";
import {
  requestLocale,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../../lib/server/proxy";
import { errorResponse, passthroughQueryParams } from "../../../../lib/server/http";
import { guardDownloadPath } from "../../../../lib/server/download";

/**
 * File download proxy.
 *
 * The proxied path is restricted to the upstream file-download prefix; any
 * other path is rejected without an upstream fetch. Returns 200 with the file
 * payload.
 */
export async function GET(request: Request): Promise<Response> {
  const locale = requestLocale(request);
  const searchParams = new URL(request.url).searchParams;

  const guard = guardDownloadPath(searchParams.get("path"));
  if (!guard.allowed) return errorResponse("api.errors.files.download", 400, locale);

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const failure = { failureLabel: "files_download_failed", errorKey: "api.errors.files.download", locale };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: guard.path,
      query: passthroughQueryParams(request, ["disposition"]),
      method: "GET",
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}
