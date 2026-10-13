import { resolveServerConfig } from "../../../../../lib/server/config";
import {
  bankScopedPath,
  requestLocale,
  serializeUpstream,
  type SerializeOptions,
} from "../../../../../lib/server/proxy";
import { errorResponse } from "../../../../../lib/server/http";

type Context = { params: Promise<{ bankId: string }> };

const TARGET_FIELD = "target_bank_id";
const SCOPE_FIELDS = ["include_data", "include_bank_config", "include_history"] as const;

/**
 * Bank clone.
 *
 * Reads the required snake_case target_bank_id and the optional boolean scope
 * flags from the JSON body, then places the target id and every
 * caller-supplied flag on the upstream QUERY STRING. No JSON body is forwarded.
 * A missing or non-string target_bank_id yields a localized 400; success is
 * 202; an internal failure is a localized 500 carrying the clone key.
 */
export async function POST(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const raw = await request.text();

  let parsed: Record<string, unknown> | undefined;
  try {
    parsed = raw.length > 0 ? (JSON.parse(raw) as Record<string, unknown>) : undefined;
  } catch {
    parsed = undefined;
  }

  const target = parsed?.[TARGET_FIELD];
  if (typeof target !== "string" || target.length === 0) {
    return errorResponse("api.errors.validation.targetBankIdRequired", 400, locale);
  }

  const query: Record<string, string | undefined> = { target_bank_id: target };
  for (const field of SCOPE_FIELDS) {
    const value = parsed?.[field];
    if (typeof value === "boolean") query[field] = value ? "true" : "false";
  }

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const options: SerializeOptions = {
    config,
    request,
    upstreamPath: bankScopedPath("/v1/default/banks", (await context.params).bankId, "/clone"),
    query,
    method: "POST",
    successStatus: 202,
    failureLabel: "clone_failed",
    errorKey: "api.errors.banks.clone",
    locale,
  };

  try {
    return await serializeUpstream(options);
  } catch {
    return errorResponse("api.errors.banks.clone", 500, locale);
  }
}