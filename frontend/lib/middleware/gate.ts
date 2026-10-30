import { prefixBasePath, stripBasePath } from "../server/paths";
import { SESSION_COOKIE, readSessionToken } from "../server/session-edge";
import { OPERATOR_SUBJECT } from "../server/subjects";
import { buildErrorBody } from "../server/errors";
import { readCookie } from "../../i18n/negotiate";
import { isApiPath, isPublicPath } from "./whitelist";

/**
 * Access-gate decision.
 *
 * Two states only: unset access key means open; set access key means enforced
 * for every non-whitelisted path. Authentication is evaluated for page requests
 * before any locale negotiation.
 */
export type GateDecision =
  | { kind: "allow" }
  | { kind: "api-unauthorized"; body: Record<string, unknown> }
  | { kind: "api-forbidden"; body: Record<string, unknown> }
  | { kind: "redirect-login"; location: string };

export type GateInput = {
  pathname: string;
  search: string;
  cookieHeader: string | undefined;
  accessKey: string;
  basePath: string;
  locale?: string;
  nowMs?: number;
};

/**
 * Whether a path is answered by the management plane's BFF and therefore needs
 * an operator session. The session's own report/exit route is exempt: it only
 * describes or clears the caller's own tenant, and a tenant session must be
 * able to leave.
 */
function requiresOperator(appPath: string): boolean {
  if (appPath === "/api/admin/session") return false;
  return appPath === "/api/admin" || appPath.startsWith("/api/admin/");
}

export async function evaluateGate(input: GateInput): Promise<GateDecision> {
  if (!input.accessKey) return { kind: "allow" };

  // When the frontend is mounted under a base path, the framework reports the
  // incoming pathname with that prefix attached. Strip it before matching the
  // fixed whitelist so public paths stay reachable and the gate stays closed
  // everywhere else.
  const appPath = stripBasePath(input.basePath, input.pathname);
  if (isPublicPath(appPath)) return { kind: "allow" };

  const token = readCookie(input.cookieHeader, SESSION_COOKIE);
  const session = await readSessionToken(input.accessKey, token, input.nowMs ?? Date.now());
  if (session) {
    // A session entered with a tenant key may use the console but never the
    // management plane: tenant administration creates, purges and re-keys other
    // tenants, which is strictly more than the data credential it was opened
    // with. The one exception is the session's own report/exit route.
    if (requiresOperator(appPath) && session.sub !== OPERATOR_SUBJECT) {
      return {
        kind: "api-forbidden",
        body: buildErrorBody("api.errors.generic.forbidden", input.locale),
      };
    }
    return { kind: "allow" };
  }

  if (isApiPath(appPath)) {
    return {
      kind: "api-unauthorized",
      body: buildErrorBody("api.errors.generic.unauthorized", input.locale),
    };
  }

  const returnTo = `${appPath}${input.search}`;
  const loginUrl = new URL(
    prefixBasePath(input.basePath, "/login"),
    "http://internal.local",
  );
  loginUrl.searchParams.set("returnTo", returnTo);
  return {
    kind: "redirect-login",
    location: `${loginUrl.pathname}${loginUrl.search}`,
  };
}