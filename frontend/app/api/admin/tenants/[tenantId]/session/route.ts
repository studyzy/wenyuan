import { adminServerConfig, resolveServerConfig } from "../../../../../../lib/server/config";
import { callUpstream, passthrough } from "../../../../../../lib/server/upstream";
import {
  TENANT_SESSION_COOKIE,
  createTenantSessionToken,
  isSecureRequest,
  tenantSessionCookieAttributes,
} from "../../../../../../lib/server/session";

type Context = { params: Promise<{ tenantId: string }> };

function sameOrigin(request: Request): boolean {
  const origin = request.headers.get("origin");
  const host = request.headers.get("host");
  if (!origin || !host) return true;
  try {
    return new URL(origin).host === host;
  } catch {
    return false;
  }
}

/**
 * Enter a tenant: mint a short-lived tenant key on the management plane and
 * place it in an encrypted, HttpOnly cookie. The key is never returned to the
 * browser; the data-plane BFF routes read it from this cookie.
 */
export async function POST(request: Request, context: Context): Promise<Response> {
  if (!sameOrigin(request)) {
    return new Response(JSON.stringify({ error: "invalid_origin" }), {
      status: 403,
      headers: { "content-type": "application/json" },
    });
  }
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  if (!config.accessKey || !config.adminKey) {
    return new Response(JSON.stringify({ error: "tenant_session_unavailable" }), {
      status: 503,
      headers: { "content-type": "application/json" },
    });
  }
  const tenantId = (await context.params).tenantId;
  const result = await callUpstream(adminServerConfig(config), {
    path: "/admin/v1/tenants/" + encodeURIComponent(tenantId) + "/session",
    method: "POST",
    headers: { "content-type": "application/json" },
    body: "{}",
  });
  if (!result.ok) return passthrough(result);

  let plaintext = "";
  try {
    const body = JSON.parse(new TextDecoder().decode(result.body)) as { plaintext?: unknown };
    if (typeof body.plaintext === "string") plaintext = body.plaintext;
  } catch {
    plaintext = "";
  }
  if (!plaintext) {
    return new Response(JSON.stringify({ error: "tenant_session_failed" }), {
      status: 502,
      headers: { "content-type": "application/json" },
    });
  }

  const token = createTenantSessionToken(config.accessKey, tenantId, plaintext, Date.now());
  const secure = isSecureRequest(request.headers.get("x-forwarded-proto"));
  return new Response(JSON.stringify({ tenant_id: tenantId }), {
    status: 201,
    headers: {
      "content-type": "application/json",
      "set-cookie":
        TENANT_SESSION_COOKIE + "=" + token + "; " + tenantSessionCookieAttributes(secure),
    },
  });
}
