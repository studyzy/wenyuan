import { resolveServerConfig } from "../../../lib/server/config";
import {
  requestLocale,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../lib/server/proxy";
import { errorResponse } from "../../../lib/server/http";

const UPSTREAM_PATH = "/v1/default/documents";

/**
 * Document collection.
 *
 * Bank and document identifiers travel in the query string by contract, never
 * as a path segment, because intermediaries normalize encoded slashes. Every
 * method requires bank_id; PATCH and DELETE additionally require document_id.
 * A missing bank id is a 400 with the bank-id-required key and a missing other
 * id is a 400 with the missing-request-data key.
 */
function readIds(request: Request): { bankId?: string; documentId?: string } {
  const searchParams = new URL(request.url).searchParams;
  const bankId = searchParams.get("bank_id") ?? undefined;
  const documentId = searchParams.get("document_id") ?? undefined;
  return { bankId: bankId || undefined, documentId: documentId || undefined };
}

function guard(
  ids: { bankId?: string; documentId?: string },
  requireDocument: boolean,
  locale: string,
): Response | undefined {
  if (!ids.bankId) return errorResponse("api.errors.validation.bankIdRequired", 400, locale);
  if (requireDocument && !ids.documentId) {
    return errorResponse("api.errors.validation.missingRequestData", 400, locale);
  }
  return undefined;
}

export async function GET(request: Request): Promise<Response> {
  const locale = requestLocale(request);
  const ids = readIds(request);
  const denied = guard(ids, false, locale);
  if (denied) return denied;

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const failure = {
    failureLabel: "documents_fetch_failed",
    errorKey: "api.errors.documents.fetchList",
    locale,
  };
  const query: Record<string, string> = { bank_id: ids.bankId as string };
  if (ids.documentId) query.document_id = ids.documentId;

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: UPSTREAM_PATH,
      query,
      method: "GET",
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}

export async function PATCH(request: Request): Promise<Response> {
  const locale = requestLocale(request);
  const ids = readIds(request);
  const denied = guard(ids, true, locale);
  if (denied) return denied;

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const body = await request.text();
  const failure = {
    failureLabel: "documents_update_tags_failed",
    errorKey: "api.errors.documents.updateTags",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: UPSTREAM_PATH,
      query: { bank_id: ids.bankId as string, document_id: ids.documentId as string },
      method: "PATCH",
      body,
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}

export async function DELETE(request: Request): Promise<Response> {
  const locale = requestLocale(request);
  const ids = readIds(request);
  const denied = guard(ids, true, locale);
  if (denied) return denied;

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const failure = {
    failureLabel: "documents_delete_failed",
    errorKey: "api.errors.documents.delete",
    locale,
  };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: UPSTREAM_PATH,
      query: { bank_id: ids.bankId as string, document_id: ids.documentId as string },
      method: "DELETE",
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}
