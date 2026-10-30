import { NextResponse, type NextRequest } from "next/server";
import { evaluateGate } from "./lib/middleware/gate";
import { isApiPath } from "./lib/middleware/whitelist";
import { resolveLocale, rewriteForLocale } from "./lib/middleware/locale";
import { LOCALE_COOKIE } from "./i18n/negotiate";
import { resolveServerConfig } from "./lib/server/config";

/**
 * Request middleware.
 *
 * The access gate is evaluated first; an unauthorized page request is
 * redirected without proceeding. API paths are handled on a dedicated branch and
 * are never locale-touched, so the proxy stays reachable regardless of the
 * active locale.
 *
 * Everything else is handed to the router under the negotiated locale. The
 * locale is never a path segment in the address bar; see `rewriteForLocale` for
 * how the request reaches the `app/[locale]/` segment without one.
 *
 * This deliberately does NOT delegate to `next-intl`'s middleware. That
 * middleware treats `localePrefix: "never"` as "strip any locale prefix I see on
 * the way in", while internally rewriting every page *into* the `[locale]`
 * segment — so the rewritten path it re-enters with looks exactly like a
 * locale-prefixed one, matches, and is redirected back to the stripped form.
 * Every page 307s to itself forever and nothing is reachable. Nothing here
 * needed what it provided: the locale is negotiated by `resolveLocale`, and both
 * the request config (`i18n/request.ts`) and the layout resolve it from the same
 * cookie, so next-intl still receives a locale without routing for it.
 */
export async function middleware(request: NextRequest): Promise<NextResponse> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const pathname = request.nextUrl.pathname;
  const cookieHeader = request.headers.get("cookie") ?? undefined;

  const localeResolution = resolveLocale({
    cookieHeader,
    acceptLanguage: request.headers.get("accept-language") ?? undefined,
  });

  const gate = await evaluateGate({
    pathname,
    search: request.nextUrl.search,
    cookieHeader,
    accessKey: config.accessKey,
    basePath: config.basePath,
    locale: localeResolution.locale,
  });

  if (gate.kind === "api-unauthorized") {
    return NextResponse.json(gate.body, { status: 401 });
  }

  if (gate.kind === "api-forbidden") {
    return NextResponse.json(gate.body, { status: 403 });
  }

  if (gate.kind === "redirect-login") {
    const url = request.nextUrl.clone();
    const [path, query] = gate.location.split("?");
    url.pathname = path;
    url.search = query ? `?${query}` : "";
    return NextResponse.redirect(url);
  }

  // The BFF route handlers live at `app/api/`, not under `app/[locale]/`, so a
  // locale rewrite would send them to `/en/api/...`, which is not a route. They
  // are also locale-independent by nature: the language is the page's concern,
  // and every handler answers in the shape its caller parses. Reach them
  // untouched, cookie mirror included — the page that issued the fetch already
  // negotiated the locale and this response carries no locale-bearing content.
  if (isApiPath(pathname)) {
    return withLocaleCookie(NextResponse.next(), cookieHeader, localeResolution.locale);
  }

  // The app tree is mounted under `app/[locale]/`, so the request has to be
  // rewritten into that segment. The rewrite is internal: the user keeps the
  // clean URL, and `stripBasePath` already ran inside the gate for matching.
  const rewritten = rewriteForLocale(pathname, localeResolution.locale);
  const url = rewritten.addedLocaleSegment
    ? request.nextUrl.clone()
    : undefined;
  if (url) url.pathname = rewritten.pathname;

  const response = url
    ? NextResponse.rewrite(url)
    : NextResponse.next();

  return withLocaleCookie(response, cookieHeader, localeResolution.locale);
}

/**
 * Mirror the negotiated locale into the cookie the request config and the layout
 * both read, so the first request of a session renders in the negotiated
 * language rather than the default. Only set when absent: an existing cookie is
 * the user's explicit choice and must win over Accept-Language.
 */
function withLocaleCookie(
  response: NextResponse,
  cookieHeader: string | undefined,
  locale: string,
): NextResponse {
  if (!cookieHeader?.includes(`${LOCALE_COOKIE}=`)) {
    response.cookies.set(LOCALE_COOKIE, locale, {
      path: "/",
      sameSite: "lax",
    });
  }
  return response;
}

export const config = {
  // Match all paths except Next.js internals and static assets.
  // - Use an explicit file extension allowlist instead of .*\..* so that
  //   dynamic segments containing dots (e.g. bank IDs like
  //   "SX.Products.GovComply.Build") are still processed.
  // - Next requires a statically analyzable literal here; it must stay
  //   byte-identical to MIDDLEWARE_MATCHER in lib/middleware/matcher.ts
  //   (asserted by test/middleware.test.ts).
  matcher:
    "/((?!_next|_vercel|.*\\.(?:png|jpe?g|gif|svg|webp|ico|css|js|map|woff2?|ttf|eot|txt|xml|json|webmanifest)$).*)",
};