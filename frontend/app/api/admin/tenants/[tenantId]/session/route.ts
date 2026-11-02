import { adminServerConfig, resolveServerConfig } from "../../../../../../lib/server/config";
import { callUpstream, passthrough } from "../../../../../../lib/server/upstream";
import {
  SESSION_COOKIE,
  TENANT_SESSION_COOKIE,
  createAdminTenantSessionToken,
  isSecureRequest,
  readSessionToken,
  tenantSessionCookieAttributes,
} from "../../../../../../lib/server/session";
import { OPERATOR_SUBJECT } from "../../../../../../lib/server/subjects";

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
 * Enter a tenant as the operator.
 *
 * The cookie names the tenant and holds no credential. Every BFF data-plane
 * call for this session attaches the operator key and names the tenant in the
 * `X-Wenyuan-Tenant` header, which the API resolves through the registry.
 * Nothing is minted here, so entering a tenant any number of times leaves the
 * tenant key table unchanged.
 *
 * The tenant must exist and be active: the API rejects a suspended or deleted
 * tenant's credential, and refusing here gives the operator that answer before
 * the dashboard fills with 401s.
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
  // The access gate already requires an operator session for /api/admin/**;
  // checking here as well keeps the route safe when it is mounted without that
  // gate, and makes the requirement explicit beside the credential it selects.
  const session = readSessionToken(
    config.accessKey,
    cookieValue(request, SESSION_COOKIE),
    Date.now(),
  );
  if (session?.sub !== OPERATOR_SUBJECT) {
    return new Response(JSON.stringify({ error: "operator_required" }), {
      status: 403,
      headers: { "content-type": "application/json" },
    });
  }

  const tenantId = (await context.params).tenantId;
  const result = await callUpstream(adminServerConfig(config), {
    path: "/admin/v1/tenants/" + encodeURIComponent(tenantId),
    method: "GET",
  });
  if (!result.ok) return passthrough(result);

  let status: unknown;
  try {
    const body = JSON.parse(new TextDecoder().decode(result.body)) as { status?: unknown };
    status = body.status;
  } catch {
    status = undefined;
  }
  if (typeof status === "string" && status !== "active") {
    return new Response(JSON.stringify({ error: "tenant_not_active", status }), {
      status: 409,
      headers: { "content-type": "application/json" },
    });
  }

  const token = createAdminTenantSessionToken(config.accessKey, tenantId, Date.now());
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