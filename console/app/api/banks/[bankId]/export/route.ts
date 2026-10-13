import { resolveServerConfig } from "../../../../../lib/server/config";
import { bankScopedPath, requestLocale, serializeUpstream } from "../../../../../lib/server/proxy";
import { booleanQueryFlags, errorResponse } from "../../../../../lib/server/http";

type Context = { params: Promise<{ bankId: string }> };

const SECTION_FLAGS = ["exclude_parents", "include_observations", "include_knowledge_base"] as const;

/**
 * Bank template export.
 *
 * Returns the template manifest object with 200. Section flags are read from
 * the query string and are set only when the raw value is exactly the string
 * "true".
 */
export async function GET(request: Request, context: Context): Promise<Response> {
  const locale = requestLocale(request);
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);

  try {
    return await serializeUpstream({
      config,
      request,
      upstreamPath: bankScopedPath("/v1/default/banks", (await context.params).bankId, "/export"),
      query: booleanQueryFlags(request, SECTION_FLAGS),
      method: "GET",
      failureLabel: "export_failed",
      errorKey: "api.errors.generic.internal",
      locale,
    });
  } catch {
    return errorResponse("api.errors.generic.internal", 500, locale);
  }
}