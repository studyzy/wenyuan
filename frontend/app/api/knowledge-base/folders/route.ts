import { resolveServerConfig } from "../../../../lib/server/config";
import {
  bankScopedPath,
  requestLocale,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../../lib/server/proxy";
import { errorResponse } from "../../../../lib/server/http";

/**
 * Knowledge-base folder creation.
 *
 * The bank id is required. Returns 200 with the upstream payload; the caller's
 * body is forwarded verbatim.
 */
export async function POST(request: Request): Promise<Response> {
  const locale = requestLocale(request);
  const bankId = new URL(request.url).searchParams.get("bank_id");
  if (!bankId) return errorResponse("api.errors.validation.bankIdRequired", 400, locale);

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const body = await request.text();
  const failure = {
    failureLabel: "knowledge_base_folder_failed",
    errorKey: "api.errors.knowledgeBase.createFolder",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: bankScopedPath("/v1/default/banks", bankId, "/knowledge-base/folders"),
      method: "POST",
      body,
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}
