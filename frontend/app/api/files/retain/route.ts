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
 * The bank id travels inside the multipart payload's `request` part, which is
 * the shape the browser client posts; a `bank_id` query parameter is also
 * accepted. The configured maximum upload size bounds the proxied body. Returns
 * 200 with the retain payload.
 */
export async function POST(request: Request): Promise<Response> {
  const locale = requestLocale(request);

  // Reading the form consumes the body, so the parse happens on a clone and the
  // original bytes are what gets forwarded — re-encoding multipart would risk a
  // boundary the upstream rejects.
  const searchParams = new URL(request.url).searchParams;
  const fromQuery = searchParams.get("bank_id") ?? searchParams.get("agent_id");

  let bankId: string | undefined;
  const formData = await request.clone().formData();
  const requestPart = formData.get("request");
  if (typeof requestPart === "string") {
    let parsed: { bank_id?: string; agent_id?: string };
    try {
      parsed = JSON.parse(requestPart) as typeof parsed;
    } catch {
      return errorResponse("api.errors.auth.invalidRequestBody", 400, locale);
    }
    bankId = parsed.bank_id || parsed.agent_id;
    if (!bankId && fromQuery === null) {
      return errorResponse("api.errors.validation.missingBankId", 400, locale);
    }
  } else if (fromQuery === null) {
    return errorResponse("api.errors.validation.missingRequestData", 400, locale);
  }

  bankId = bankId ?? fromQuery ?? undefined;
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
