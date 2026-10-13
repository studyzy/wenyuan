import { resolveServerConfig } from "../../../../lib/server/config";
import {
  requestLocale,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../../lib/server/proxy";
import { errorResponse } from "../../../../lib/server/http";

/**
 * Document reprocess.
 *
 * Bank and document identifiers travel in the query string by contract. The
 * caller's body is forwarded when present.
 */
export async function POST(request: Request): Promise<Response> {
  const locale = requestLocale(request);
  const searchParams = new URL(request.url).searchParams;
  const bankId = searchParams.get("bank_id");
  if (!bankId) return errorResponse("api.errors.validation.bankIdRequired", 400, locale);
  const documentId = searchParams.get("document_id");
  if (!documentId) {
    return errorResponse("api.errors.validation.missingRequestData", 400, locale);
  }

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const body = await request.text();
  const failure = {
    failureLabel: "documents_reprocess_failed",
    errorKey: "api.errors.documents.reprocess",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: "/v1/default/documents/reprocess",
      query: { bank_id: bankId, document_id: documentId },
      method: "POST",
      body: body.length > 0 ? body : undefined,
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}
