import { resolveServerConfig } from "../../../../lib/server/config";
import {
  requestLocale,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../../lib/server/proxy";
import { errorResponse } from "../../../../lib/server/http";

/**
 * Document chunk listing.
 *
 * Bank and document identifiers travel in the query string by contract. The
 * limit and offset parameters carry their documented defaults when the caller
 * omits them.
 */
export async function GET(request: Request): Promise<Response> {
  const locale = requestLocale(request);
  const searchParams = new URL(request.url).searchParams;
  const bankId = searchParams.get("bank_id");
  if (!bankId) return errorResponse("api.errors.validation.bankIdRequired", 400, locale);
  const documentId = searchParams.get("document_id");
  if (!documentId) {
    return errorResponse("api.errors.validation.missingRequestData", 400, locale);
  }

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const failure = {
    failureLabel: "documents_chunks_failed",
    errorKey: "api.errors.documents.chunks",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: "/v1/default/documents/chunks",
      query: {
        bank_id: bankId,
        document_id: documentId,
        limit: searchParams.get("limit") || "100",
        offset: searchParams.get("offset") || "0",
      },
      method: "GET",
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}
