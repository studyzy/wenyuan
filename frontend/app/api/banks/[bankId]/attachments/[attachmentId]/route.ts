import { resolveServerConfig } from "../../../../../../lib/server/config";
import { bankScopedPath, requestLocale } from "../../../../../../lib/server/proxy";
import { errorResponse } from "../../../../../../lib/server/http";
import { callUpstream } from "../../../../../../lib/server/upstream";
import {
  FALLBACK_BINARY_CONTENT_TYPE,
  isAttachmentId,
} from "../../../../../../lib/server/attachment";

type Context = { params: Promise<{ bankId: string; attachmentId: string }> };

/**
 * Inline attachment byte proxy.
 *
 * The id must be a fixed-length lowercase hexadecimal value; anything else is
 * rejected with a 400 before any upstream fetch, so the route cannot be used
 * to steer the proxied upstream path. On success the upstream content type is
 * passed through (or a generic binary type) along with a no-sniff header and a
 * long-lived immutable cache directive. The upstream status is passed through,
 * so the success status is whatever the upstream returns.
 */
export async function GET(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const { bankId, attachmentId } = await context.params;

  if (!isAttachmentId(attachmentId)) {
    return errorResponse("api.errors.validation.missingRequestData", 400, locale);
  }

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const result = await callUpstream(config, {
    path: bankScopedPath(
      "/v1/default/banks",
      bankId,
      `/attachments/${attachmentId}`,
    ),
    method: "GET",
  });

  const headers = new Headers();
  headers.set("content-type", result.contentType ?? FALLBACK_BINARY_CONTENT_TYPE);
  headers.set("x-content-type-options", "nosniff");
  headers.set("cache-control", "private, max-age=31536000, immutable");
  if (result.contentDisposition) headers.set("content-disposition", result.contentDisposition);
  return new Response(result.body, { status: result.status, headers });
}