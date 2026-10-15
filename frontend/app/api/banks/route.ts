import { resolveServerConfig } from "../../../lib/server/config";
import {
  bankScopedPath,
  requestLocale,
  serializeUpstream,
  upstreamUnavailableBody,
} from "../../../lib/server/proxy";
import { errorResponse, passthroughQueryParams } from "../../../lib/server/http";

const LIST_PARAMS = ["q", "limit", "offset"] as const;

/**
 * Bank collection.
 *
 * GET returns 200 with a banks list plus total, limit and offset, accepting a
 * search term and numeric pagination. POST creates a bank and returns 201: the
 * body carries only the bank id, and creation is an upsert on the bank's own
 * resource, because the collection itself has no create operation. An
 * unparseable body is a 400.
 */
export async function GET(request: Request): Promise<Response> {
  const locale = requestLocale(request);
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const failure = { failureLabel: "banks_fetch_failed", errorKey: "api.errors.banks.fetch", locale };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: "/v1/default/banks",
      query: passthroughQueryParams(request, LIST_PARAMS),
      method: "GET",
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}

export async function POST(request: Request): Promise<Response> {
  const locale = requestLocale(request);
  const raw = await request.text();

  let parsed: Record<string, unknown>;
  try {
    parsed = JSON.parse(raw) as Record<string, unknown>;
  } catch {
    return errorResponse("api.errors.auth.invalidRequestBody", 400, locale);
  }

  const bankId = parsed?.bank_id;
  if (typeof bankId !== "string" || bankId.length === 0) {
    return errorResponse("api.errors.validation.bankIdRequired", 400, locale);
  }

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const failure = { failureLabel: "banks_create_failed", errorKey: "api.errors.banks.create", locale };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: bankScopedPath("/v1/default/banks", bankId),
      method: "PUT",
      // The collection takes no create body; the id is what the caller chose and
      // the bank is created empty.
      body: "{}",
      successStatus: 201,
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}
