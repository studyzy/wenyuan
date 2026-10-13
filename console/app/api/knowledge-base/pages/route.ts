import { resolveServerConfig } from "../../../../lib/server/config";
import {
  bankScopedPath,
  requestLocale,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../../lib/server/proxy";
import { errorResponse } from "../../../../lib/server/http";

/**
 * Knowledge-base page save.
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
    failureLabel: "knowledge_base_page_failed",
    errorKey: "api.errors.knowledgeBase.createPage",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: bankScopedPath("/v1/default/banks", bankId, "/knowledge-base/pages"),
      method: "POST",
      body,
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}
