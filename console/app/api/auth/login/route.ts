import { resolveServerConfig } from "../../../../lib/server/config";
import {
  SESSION_COOKIE,
  createSessionToken,
  isSecureRequest,
  sessionCookieAttributes,
  verifyAccessKey,
} from "../../../../lib/server/session";
import { errorResponse } from "../../../../lib/server/http";
import { negotiateLocale } from "../../../../i18n/negotiate";

/**
 * Login route. Binds POST, returning documented localized errors and issuing
 * the signed session cookie on success.
 *
 * The submitted key travels in the `key` field, which is what the login form
 * posts; reading any other name would leave the form unable to sign in.
 */
export async function POST(request: Request): Promise<Response> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const locale = negotiateLocale({
    cookieHeader: request.headers.get("cookie") ?? undefined,
    acceptLanguage: request.headers.get("accept-language") ?? undefined,
  });

  if (!config.accessKey) {
    return errorResponse("api.errors.auth.keyNotConfigured", 503, locale);
  }

  let payload: { key?: string } | undefined;
  try {
    payload = (await request.json()) as { key?: string };
  } catch {
    return errorResponse("api.errors.auth.invalidRequestBody", 400, locale);
  }

  const submitted = typeof payload?.key === "string" ? payload.key : "";
  if (!verifyAccessKey(config.accessKey, submitted)) {
    return errorResponse("api.errors.auth.wrongKey", 401, locale);
  }

  const token = createSessionToken(config.accessKey, "console", Date.now());
  const secure = isSecureRequest(request.headers.get("x-forwarded-proto"));
  const headers = new Headers({
    "content-type": "application/json; charset=utf-8",
    "set-cookie": `${SESSION_COOKIE}=${token}; ${sessionCookieAttributes(secure)}`,
  });
  return new Response(JSON.stringify({ signedIn: true }), { status: 200, headers });
}