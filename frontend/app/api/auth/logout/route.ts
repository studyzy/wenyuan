import { SESSION_COOKIE, clearSessionCookieAttributes, isSecureRequest } from "../../../../lib/server/session";

/**
 * Logout route. Binds POST, clearing the session cookie with max age zero.
 */
export async function POST(request: Request): Promise<Response> {
  const secure = isSecureRequest(request.headers.get("x-forwarded-proto"));
  const headers = new Headers({
    "content-type": "application/json; charset=utf-8",
    "set-cookie": `${SESSION_COOKIE}=; ${clearSessionCookieAttributes(secure)}`,
  });
  return new Response(JSON.stringify({ signedOut: true }), { status: 200, headers });
}