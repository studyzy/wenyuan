import { NextResponse, type NextRequest } from "next/server";
import createIntlMiddleware from "next-intl/middleware";
import { routing } from "./i18n/routing";
import { evaluateGate } from "./lib/middleware/gate";
import { resolveLocale } from "./lib/middleware/locale";
import { stripBasePath } from "./lib/server/paths";
import { resolveServerConfig } from "./lib/server/config";

const intlMiddleware = createIntlMiddleware(routing);

/**
 * Request middleware.
 *
 * Order for page requests: the access gate is evaluated first; an unauthorized
 * page request is redirected without proceeding to locale negotiation. API
 * paths are handled on a dedicated branch and are never locale-rewritten, so the
 * proxy stays reachable regardless of the active locale. Everything else is
 * handed to next-intl, which negotiates the locale and rewrites internally into
 * the `[locale]` segment while `localePrefix: "never"` keeps the URL clean.
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

  if (gate.kind === "redirect-login") {
    const url = request.nextUrl.clone();
    const [path, query] = gate.location.split("?");
    url.pathname = path;
    url.search = query ? `?${query}` : "";
    return NextResponse.redirect(url);
  }

  // API routes carry no locale segment and must not be locale-rewritten.
  const appPathname = stripBasePath(config.basePath, pathname);
  if (appPathname === "/api" || appPathname.startsWith("/api/")) {
    return NextResponse.next();
  }

  return intlMiddleware(request);
}

export const config = {
  // Match all paths except Next.js internals and static assets.
  // - Use an explicit file extension allowlist instead of .*\..* so that
  //   dynamic segments containing dots (e.g. bank IDs like
  //   "SX.Products.GovComply.Build") still get the i18n locale rewrite.
  // - Next requires a statically analyzable literal here; it must stay
  //   byte-identical to MIDDLEWARE_MATCHER in lib/middleware/matcher.ts
  //   (asserted by test/middleware.test.ts).
  matcher:
    "/((?!_next|_vercel|.*\\.(?:png|jpe?g|gif|svg|webp|ico|css|js|map|woff2?|ttf|eot|txt|xml|json)$).*)",
};