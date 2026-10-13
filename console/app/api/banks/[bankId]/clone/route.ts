import { resolveServerConfig } from "../../../../../lib/server/config";
import { bankScopedPath, proxyUpstream, requestLocale } from "../../../../../lib/server/proxy";
import { errorResponse } from "../../../../../lib/server/http";

type Context = { params: Promise<{ bankId: string }> };

/**
 * Bank clone.
 *
 * Requires a string target-bank-id body field; the optional boolean scope
 * flags for data, bank config, and history are forwarded only when the caller
 * sets them. A missing or non-string target-bank-id yields a localized 400.
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

  const target = parsed?.["target-bank-id"];
  if (typeof target !== "string" || target.length === 0) {
    return errorResponse("errors.requiredTargetBankId", 400, locale);
  }

  const body: Record<string, unknown> = { "target-bank-id": target };
  for (const key of ["copy-data", "copy-config", "copy-history"]) {
    if (typeof parsed?.[key] === "boolean") body[key] = parsed[key];
  }

  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  return proxyUpstream({
    config,
    request,
    upstreamPath: bankScopedPath("/v1/default/banks", (await context.params).bankId, "/clone"),
    method: "POST",
    body: JSON.stringify(body),
  });
}