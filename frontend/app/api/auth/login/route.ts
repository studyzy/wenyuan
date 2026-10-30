import { adminServerConfig, resolveServerConfig } from "../../../../lib/server/config";
import {
  SESSION_COOKIE,
  TENANT_SESSION_COOKIE,
  createSessionToken,
  createTenantSessionToken,
  isSecureRequest,
  sessionCookieAttributes,
  tenantSessionCookieAttributes,
  verifyAccessKey,
} from "../../../../lib/server/session";
import { errorResponse } from "../../../../lib/server/http";
import { callUpstream } from "../../../../lib/server/upstream";
import { negotiateLocale } from "../../../../i18n/negotiate";

/** Prefix every key the registry mints carries (internal/tenant.NewKey). */
const TENANT_KEY_PREFIX = "wyk_";

type TenantResolution =
  | { kind: "tenant"; tenantId: string }
  | { kind: "unauthorized" }
  | { kind: "unavailable" };

/**
 * Resolve a submitted key as a tenant credential.
 *
 * The console key and a tenant key are two different things: the console key
 * unlocks the console, while a tenant key is a data-plane bearer credential
 * that names the schema to serve. The management plane owns the key-to-tenant
 * mapping, and it answers with the same resolver the data plane authenticates
 * with, so a key that resolves here is one the API accepts.
 */
async function resolveTenant(
  config: ReturnType<typeof resolveServerConfig>,
  key: string,
): Promise<TenantResolution> {
  if (!config.adminKey) return { kind: "unavailable" };
  try {
    const result = await callUpstream(adminServerConfig(config), {
      path: "/admin/v1/tenants/resolve",
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ key }),
    });
    if (!result.ok) {
      // A rejected credential is a wrong key; anything else (501 in a
      // single-tenant deployment, an unreachable registry) means this
      // deployment cannot accept tenant-key sign-in at all.
      return result.status === 401 ? { kind: "unauthorized" } : { kind: "unavailable" };
    }
    const body = JSON.parse(new TextDecoder().decode(result.body)) as { tenant_id?: unknown };
    if (typeof body.tenant_id === "string" && body.tenant_id) {
      return { kind: "tenant", tenantId: body.tenant_id };
    }
    return { kind: "unavailable" };
  } catch {
    return { kind: "unavailable" };
  }
}

/**
 * Login route. Binds POST, returning documented localized errors and issuing the
 * signed session cookie on success.
 *
 * The submitted key travels in the `key` field, which is what the login form
 * posts; reading any other name would leave the form unable to sign in.
 *
 * A key that is not the console key but carries the tenant registry prefix is
 * tried as a tenant key: on success the session is entered as that tenant, so
 * the console serves the tenant schema instead of the deployment default. The
 * tenant credential is sealed in its own cookie and never reaches browser
 * JavaScript.
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
  const secure = isSecureRequest(request.headers.get("x-forwarded-proto"));
  const headers = new Headers({ "content-type": "application/json; charset=utf-8" });

  if (verifyAccessKey(config.accessKey, submitted)) {
    headers.append(
      "set-cookie",
      `${SESSION_COOKIE}=${createSessionToken(config.accessKey, "console", Date.now())}; ${sessionCookieAttributes(secure)}`,
    );
    return new Response(JSON.stringify({ signedIn: true }), { status: 200, headers });
  }

  // Tenant keys carry the registry prefix. Anything else is a mistyped console
  // key, and asking the management plane about it would turn a typo into a
  // deployment-capability error.
  if (!submitted.startsWith(TENANT_KEY_PREFIX)) {
    return errorResponse("api.errors.auth.wrongKey", 401, locale);
  }

  const tenant = await resolveTenant(config, submitted);
  if (tenant.kind === "unauthorized") {
    return errorResponse("api.errors.auth.wrongKey", 401, locale);
  }
  if (tenant.kind === "unavailable") {
    return errorResponse("api.errors.auth.tenantLoginUnavailable", 503, locale);
  }

  // The subject is what keeps this session out of the management plane: a
  // tenant key must not be able to create, purge or re-key tenants just because
  // it can use the console.
  headers.append(
    "set-cookie",
    `${SESSION_COOKIE}=${createSessionToken(config.accessKey, `tenant:${tenant.tenantId}`, Date.now())}; ${sessionCookieAttributes(secure)}`,
  );
  headers.append(
    "set-cookie",
    `${TENANT_SESSION_COOKIE}=${createTenantSessionToken(config.accessKey, tenant.tenantId, submitted, Date.now())}; ${tenantSessionCookieAttributes(secure)}`,
  );
  return new Response(JSON.stringify({ signedIn: true, tenant_id: tenant.tenantId }), {
    status: 200,
    headers,
  });
}
