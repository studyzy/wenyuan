import { resolveServerConfig } from "../../../../lib/server/config";
import {
  SESSION_COOKIE,
  TENANT_SESSION_COOKIE,
  clearTenantSessionCookieAttributes,
  isSecureRequest,
  readSessionToken,
  readTenantSessionToken,
} from "../../../../lib/server/session";
import { OPERATOR_SUBJECT } from "../../../../lib/server/subjects";

function cookieValue(request: Request, name: string): string | undefined {
  const header = request.headers.get("cookie");
  if (!header) return undefined;
  for (const part of header.split(";")) {
    const [key, ...rest] = part.trim().split("=");
    if (key === name) return rest.join("=");
  }
  return undefined;
}

/**
 * Report which tenant the console session is entered as, never its key, and
 * whether the session is the operator's.
 *
 * `operator` decides which console links the nav offers. A tenant key opens one
 * tenant's schema and is refused by the management plane, so the tenant, team
 * and overview entries would only lead that session to a 403.
 */
export async function GET(request: Request): Promise<Response> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  // No access key means the gate is open, which is also when every page is
  // reachable, so the console keeps its full navigation.
  const session = config.accessKey
    ? readSessionToken(config.accessKey, cookieValue(request, SESSION_COOKIE), Date.now())
    : null;
  const operator = !config.accessKey || session?.sub === OPERATOR_SUBJECT;

  const token = cookieValue(request, TENANT_SESSION_COOKIE);
  if (!config.accessKey || !token) {
    return Response.json({ tenant_id: null, operator });
  }
  const tenant = readTenantSessionToken(config.accessKey, token, Date.now());
  return Response.json({ tenant_id: tenant?.tenant_id ?? null, operator });
}

/** Leave the tenant: the cookie is cleared and the static console resumes. */
export async function DELETE(request: Request): Promise<Response> {
  const secure = isSecureRequest(request.headers.get("x-forwarded-proto"));
  return new Response(JSON.stringify({ tenant_id: null }), {
    status: 200,
    headers: {
      "content-type": "application/json",
      "set-cookie":
        TENANT_SESSION_COOKIE + "=; " + clearTenantSessionCookieAttributes(secure),
    },
  });
}