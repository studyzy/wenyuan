import { prefixBasePath, stripBasePath } from "../server/paths";
import { verifySessionToken, SESSION_COOKIE } from "../server/session-edge";
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

export async function evaluateGate(input: GateInput): Promise<GateDecision> {
  if (!input.accessKey) return { kind: "allow" };

  // When the console is mounted under a base path, the framework reports the
  // incoming pathname with that prefix attached. Strip it before matching the
  // fixed whitelist so public paths stay reachable and the gate stays closed
  // everywhere else.
  const appPath = stripBasePath(input.basePath, input.pathname);
  if (isPublicPath(appPath)) return { kind: "allow" };

  const token = readCookie(input.cookieHeader, SESSION_COOKIE);
  if (await verifySessionToken(input.accessKey, token, input.nowMs ?? Date.now())) {
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