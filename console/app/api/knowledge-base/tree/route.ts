import { resolveServerConfig } from "../../../../lib/server/config";
import {
  bankScopedPath,
  requestLocale,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../../lib/server/proxy";
import { errorResponse, tagFilterParams } from "../../../../lib/server/http";

/**
 * Knowledge-base tree.
 *
 * The bank id is required. Returns 200 with the tree payload and accepts a tag
 * filter.
 */
export async function GET(request: Request): Promise<Response> {
  const locale = requestLocale(request);
  const searchParams = new URL(request.url).searchParams;
  const bankId = searchParams.get("bank_id");
  if (!bankId) return errorResponse("api.errors.validation.bankIdRequired", 400, locale);

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const failure = {
    failureLabel: "knowledge_base_tree_failed",
    errorKey: "api.errors.knowledgeBase.tree",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: bankScopedPath("/v1/default/banks", bankId, "/knowledge-base/tree"),
      query: tagFilterParams(request),
      method: "GET",
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}
