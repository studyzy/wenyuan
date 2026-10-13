import { resolveServerConfig } from "../../../../lib/server/config";
import {
  bankScopedPath,
  requestLocale,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../../lib/server/proxy";
import { errorResponse } from "../../../../lib/server/http";

/**
 * Multipart file retain for document upload.
 *
 * The bank id is required. The configured maximum upload size bounds the
 * proxied body. Returns 200 with the retain payload.
 */
export async function POST(request: Request): Promise<Response> {
  const locale = requestLocale(request);
  const searchParams = new URL(request.url).searchParams;
  const bankId = searchParams.get("bank_id") ?? searchParams.get("agent_id");
  if (!bankId) return errorResponse("api.errors.validation.bankIdRequired", 400, locale);

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const declared = Number.parseInt(request.headers.get("content-length") ?? "", 10);
  if (Number.isFinite(declared) && declared > config.maxUploadSize) {
    return errorResponse("api.errors.files.upload", 400, locale);
  }

  const body = await request.arrayBuffer();
  if (body.byteLength > config.maxUploadSize) {
    return errorResponse("api.errors.files.upload", 400, locale);
  }

  const failure = { failureLabel: "files_upload_failed", errorKey: "api.errors.files.upload", locale };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: bankScopedPath("/v1/default/banks", bankId, "/files/retain"),
      method: "POST",
      body,
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}
