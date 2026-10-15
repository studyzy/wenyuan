import { resolveServerConfig } from "../../../lib/server/config";
import {
  bankScopedPath,
  requestLocale,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../lib/server/proxy";
import { errorResponse, TAGS_MATCH_MODES } from "../../../lib/server/http";

/** Time axes the document listing accepts; anything else is dropped, not forwarded. */
const TIME_FIELDS = ["created_at", "updated_at"] as const;

const LIST_PARAMS = ["q", "limit", "offset", "start_date", "end_date"] as const;

/**
 * The document member lives under a trailing-slash subtree upstream because a
 * document id may itself contain slashes.
 */
function documentMemberPath(bankId: string, documentId: string): string {
  return bankScopedPath(
    "/v1/default/banks",
    bankId,
    `/documents/${encodeURIComponent(documentId)}`,
  );
}

/**
 * Document collection.
 *
 * Bank and document identifiers travel in the query string by contract, never
 * as a path segment of this route, because intermediaries normalize encoded
 * slashes. Every method requires bank_id; PATCH and DELETE additionally require
 * document_id. A missing bank id is a 400 with the bank-id-required key and a
 * missing other id is a 400 with the missing-request-data key.
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

/** The collection filters, shaped so an unknown enum is a dropped parameter rather than a 422. */
function listQuery(request: Request): Record<string, string | string[]> {
  const searchParams = new URL(request.url).searchParams;
  const query: Record<string, string | string[]> = {};

  const tags = searchParams.getAll("tags").filter((tag) => tag.length > 0);
  if (tags.length > 0) query.tags = tags;

  const mode = searchParams.get("tags_match");
  if (tags.length > 0 && mode && (TAGS_MATCH_MODES as readonly string[]).includes(mode)) {
    query.tags_match = mode;
  }

  const timeField = searchParams.get("time_field");
  if (timeField && (TIME_FIELDS as readonly string[]).includes(timeField)) {
    query.time_field = timeField;
  }

  for (const name of LIST_PARAMS) {
    const value = searchParams.get(name);
    if (value !== null && value.length > 0) query[name] = value;
  }

  return query;
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

  try {
    // A document id narrows the read to one member; the listing filters do not
    // apply to that member, so the two shapes never mix.
    if (ids.documentId) {
      return await serializeUpstream({
        config,
        request,
        upstreamPath: documentMemberPath(ids.bankId as string, ids.documentId),
        method: "GET",
        ...failure,
      });
    }

    return await serializeUpstream({
      config,
      request,
      upstreamPath: bankScopedPath("/v1/default/banks", ids.bankId as string, "/documents"),
      query: listQuery(request),
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
      upstreamPath: documentMemberPath(ids.bankId as string, ids.documentId as string),
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
      upstreamPath: documentMemberPath(ids.bankId as string, ids.documentId as string),
      method: "DELETE",
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}