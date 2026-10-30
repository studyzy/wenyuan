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

/** Prefix every tenant key the registry mints carries (internal/tenant.NewKey). */
const TENANT_KEY_PREFIX = "wyk_";

/** Operator session: may reach the management plane. */
const OPERATOR_SUBJECT = "console";
/** Data-key session: may use the console, never the management plane. */
const DATA_SUBJECT = "data";

type TenantResolution =
  | { kind: "tenant"; tenantId: string }
  | { kind: "unauthorized" }
  | { kind: "unavailable" };

/**
 * Resolve a submitted key as a tenant credential.
 *
 * The data key and a tenant key are different things: the data key names the
 * deployment's default schema, a tenant key names one tenant. The management
 * plane owns the key-to-tenant mapping and answers with the same resolver the
 * data plane authenticates with, so a key that resolves here is one the API
 * accepts.
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
 * Whether the data plane accepts the configured data key.
 *
 * A multi-tenant deployment ignores the static key: the key is right for this
 * configuration and still refused by the API. Asking once at sign-in turns that
 * into a rejected login instead of a console whose every page is empty. An
 * unreachable API is not an authentication answer, so it leaves the sign-in
 * alone and lets the pages report the outage.
 */
async function dataPlaneRefusesKey(
  config: ReturnType<typeof resolveServerConfig>,
  key: string,
): Promise<boolean> {
  try {
    const result = await callUpstream({ ...config, dataplaneKey: key }, {
      path: "/v1/default/banks",
      query: { limit: "1" },
      method: "GET",
    });
    return result.status === 401;
  } catch {
    return false;
  }
}

/**
 * Login route. Binds POST, returning documented localized errors and issuing the
 * signed session cookie on success.
 *
 * The submitted key travels in the `key` field, which is what the login form
 * posts. Two credentials open the console, and they are the two the deployment
 * already holds:
 *
 *   - the operator key (`WENYUAN_CP_ADMIN_API_KEY`) — tenant administration;
 *   - the data key (`WENYUAN_CP_DATAPLANE_API_KEY`) — the console and the API;
 *   - a tenant key (`wyk_...`) — one tenant's schema, through the registry.
 *
 * The session records which one it was: only the operator session may reach the
 * management plane, so holding a data or tenant credential never widens into
 * tenant administration.
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

  const signInAs = (
    subject: string,
    options: { cookies?: string[]; body?: Record<string, unknown> } = {},
  ): Response => {
    const headers = new Headers({ "content-type": "application/json; charset=utf-8" });
    headers.append(
      "set-cookie",
      `${SESSION_COOKIE}=${createSessionToken(config.accessKey, subject, Date.now())}; ${sessionCookieAttributes(secure)}`,
    );
    for (const cookie of options.cookies ?? []) headers.append("set-cookie", cookie);
    return new Response(JSON.stringify(options.body ?? { signedIn: true }), {
      status: 200,
      headers,
    });
  };

  if (config.adminKey && verifyAccessKey(config.adminKey, submitted)) {
    return signInAs(OPERATOR_SUBJECT);
  }

  if (config.dataplaneKey && verifyAccessKey(config.dataplaneKey, submitted)) {
    if (await dataPlaneRefusesKey(config, submitted)) {
      return errorResponse("api.errors.auth.dataKeyLoginUnavailable", 401, locale);
    }
    return signInAs(DATA_SUBJECT);
  }

  // Tenant keys carry the registry prefix. Anything else is a mistyped key, and
  // asking the management plane about it would turn a typo into a
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
  // tenant key must not create, purge or re-key tenants just because it can use
  // the console. The key itself is sealed in the tenant cookie for the BFF.
  return signInAs(`tenant:${tenant.tenantId}`, {
    cookies: [
      `${TENANT_SESSION_COOKIE}=${createTenantSessionToken(config.accessKey, tenant.tenantId, submitted, Date.now())}; ${tenantSessionCookieAttributes(secure)}`,
    ],
    body: { signedIn: true, tenant_id: tenant.tenantId },
  });
}
