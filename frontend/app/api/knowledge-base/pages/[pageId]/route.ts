import { resolveServerConfig } from "../../../../../lib/server/config";
import {
  bankScopedPath,
  requestLocale,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../../../lib/server/proxy";
import { errorResponse } from "../../../../../lib/server/http";

type Context = { params: Promise<{ pageId: string }> };

/**
 * Knowledge-base page read.
 *
 * The bank id is required. The page id is a path segment on the upstream call.
 * Returns 200 with the page payload.
 */
export async function GET(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const bankId = new URL(request.url).searchParams.get("bank_id");
  if (!bankId) return errorResponse("api.errors.validation.bankIdRequired", 400, locale);

  const pageId = (await context.params).pageId;
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const failure = {
    failureLabel: "knowledge_base_page_fetch_failed",
    errorKey: "api.errors.knowledgeBase.fetchPage",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: bankScopedPath(
        "/v1/default/banks",
        bankId,
        `/knowledge-base/pages/${encodeURIComponent(pageId)}`,
      ),
      method: "GET",
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}
