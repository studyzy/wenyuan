import { resolveServerConfig } from "../../../../../lib/server/config";
import {
  bankScopedPath,
  requestLocale,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../../../lib/server/proxy";
import { errorResponse } from "../../../../../lib/server/http";

type Context = { params: Promise<{ nodeId: string }> };

function nodePath(bankId: string, nodeId: string): string {
  return bankScopedPath(
    "/v1/default/banks",
    bankId,
    `/knowledge-base/nodes/${encodeURIComponent(nodeId)}`,
  );
}

/**
 * Knowledge-base node sub-resource: PATCH updates, DELETE removes.
 *
 * The bank id is required. Both return 200 with the upstream payload.
 */
export async function PATCH(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const bankId = new URL(request.url).searchParams.get("bank_id");
  if (!bankId) return errorResponse("api.errors.validation.bankIdRequired", 400, locale);

  const nodeId = (await context.params).nodeId;
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const body = await request.text();
  const failure = {
    failureLabel: "knowledge_base_node_update_failed",
    errorKey: "api.errors.knowledgeBase.updateNode",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: nodePath(bankId, nodeId),
      method: "PATCH",
      body,
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}

export async function DELETE(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const bankId = new URL(request.url).searchParams.get("bank_id");
  if (!bankId) return errorResponse("api.errors.validation.bankIdRequired", 400, locale);

  const nodeId = (await context.params).nodeId;
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const failure = {
    failureLabel: "knowledge_base_node_delete_failed",
    errorKey: "api.errors.knowledgeBase.deleteNode",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: nodePath(bankId, nodeId),
      method: "DELETE",
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}
