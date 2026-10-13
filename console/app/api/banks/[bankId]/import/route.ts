import { resolveServerConfig } from "../../../../../lib/server/config";
import { bankScopedPath, requestLocale, serializeUpstream, upstreamUnavailableBody } from "../../../../../lib/server/proxy";
import { booleanQueryFlags, errorResponse } from "../../../../../lib/server/http";

type Context = { params: Promise<{ bankId: string }> };

const IMPORT_FLAGS = ["dry_run"] as const;

/**
 * Bank template import.
 *
 * Returns the import result with 200. The dry-run flag is read from the query
 * string and is set only when the raw value is exactly the string "true"; any
 * other value leaves it unset so the upstream default applies.
 */
export async function POST(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const body = await request.text();

  const failure = { failureLabel: "import_failed", errorKey: "api.errors.generic.internal", locale };

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: bankScopedPath("/v1/default/banks", (await context.params).bankId, "/import"),
      query: booleanQueryFlags(request, IMPORT_FLAGS),
      method: "POST",
      body,
      ...failure,
    });
  } catch {
    return upstreamUnavailableBody(failure);
  }
}