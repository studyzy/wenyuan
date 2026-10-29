import { resolveServerConfig } from "../../../../lib/server/config";
import {
  TENANT_SESSION_COOKIE,
  clearTenantSessionCookieAttributes,
  isSecureRequest,
  readTenantSessionToken,
} from "../../../../lib/server/session";

function cookieValue(request: Request, name: string): string | undefined {
  const header = request.headers.get("cookie");
  if (!header) return undefined;
  for (const part of header.split(";")) {
    const [key, ...rest] = part.trim().split("=");
    if (key === name) return rest.join("=");
  }
  return undefined;
}

/** Report which tenant the console session is entered as, never its key. */
export async function GET(request: Request): Promise<Response> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const token = cookieValue(request, TENANT_SESSION_COOKIE);
  if (!config.accessKey || !token) {
    return Response.json({ tenant_id: null });
  }
  const session = readTenantSessionToken(config.accessKey, token, Date.now());
  return Response.json({ tenant_id: session?.tenant_id ?? null });
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
