import { resolveServerConfig } from "../../../../lib/server/config";
import {
  bankScopedPath,
  requestLocale,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../../lib/server/proxy";
import { errorResponse } from "../../../../lib/server/http";

/**
 * Synchronous retain.
 *
 * The bank id is taken from the JSON body or the bank_id query parameter. A
 * multipart body is forwarded as received. Returns 200 with the client
 * response.
 */
export async function POST(request: Request): Promise<Response> {
  const locale = requestLocale(request);
  const contentType = request.headers.get("content-type") ?? "";
  const isMultipart = contentType.includes("multipart/form-data");
  const body = await request.arrayBuffer();

  let bankId: string | undefined;
  if (isMultipart) {
    bankId = new URL(request.url).searchParams.get("bank_id") ?? undefined;
  } else {
    try {
      const parsed = JSON.parse(new TextDecoder().decode(body)) as Record<string, unknown>;
      const fromBody = parsed?.bank_id;
      if (typeof fromBody === "string" && fromBody.length > 0) bankId = fromBody;
    } catch {
      // Malformed JSON is left to the upstream to reject.
    }
    if (!bankId) bankId = new URL(request.url).searchParams.get("bank_id") ?? undefined;
  }

  if (!bankId) return errorResponse("api.errors.validation.bankIdRequired", 400, locale);

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const failure = {
    failureLabel: "memories_retain_failed",
    errorKey: "api.errors.memories.retain",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: bankScopedPath("/v1/default/banks", bankId, "/memories"),
      method: "POST",
      body,
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}
