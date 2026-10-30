import {
  SESSION_COOKIE,
  TENANT_SESSION_COOKIE,
  clearSessionCookieAttributes,
  clearTenantSessionCookieAttributes,
  isSecureRequest,
} from "../../../../lib/server/session";

/**
 * Logout route. Binds POST, clearing both cookies with max age zero.
 *
 * The tenant cookie is cleared too: a session entered as a tenant must not
 * survive a sign-out, or the next console sign-in would silently proxy to the
 * previous tenant's schema.
 */
export async function POST(request: Request): Promise<Response> {
  const secure = isSecureRequest(request.headers.get("x-forwarded-proto"));
  const headers = new Headers({ "content-type": "application/json; charset=utf-8" });
  headers.append("set-cookie", `${SESSION_COOKIE}=; ${clearSessionCookieAttributes(secure)}`);
  headers.append(
    "set-cookie",
    `${TENANT_SESSION_COOKIE}=; ${clearTenantSessionCookieAttributes(secure)}`,
  );
  return new Response(JSON.stringify({ signedOut: true }), { status: 200, headers });
}
