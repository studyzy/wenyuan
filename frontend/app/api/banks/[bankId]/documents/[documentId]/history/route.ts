import { resolveServerConfig } from "../../../../../../../lib/server/config";
import {
  bankScopedPath,
  requestLocale,
  searchParamsObject,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../../../../../lib/server/proxy";
import { errorResponse } from "../../../../../../../lib/server/http";

type Context = { params: Promise<{ bankId: string; documentId: string }> };

function documentHistoryPath(bankId: string, documentId: string): string {
  return bankScopedPath(
    "/v1/default/banks",
    bankId,
    `/documents/${encodeURIComponent(documentId)}/history`,
  );
}

/**
 * A document's change history: what each retain, fact edit and consolidation
 * did to its facts and observations, newest first. Pagination counts
 * operations, not events.
 */
export async function GET(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const { bankId, documentId } = await context.params;
  if (!bankId || !documentId) {
    return errorResponse("api.errors.validation.documentIdRequired", 400, locale);
  }

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const failure = {
    failureLabel: "document_history_failed",
    errorKey: "api.errors.documents.history",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: documentHistoryPath(bankId, documentId),
      query: searchParamsObject(request),
      method: "GET",
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}
