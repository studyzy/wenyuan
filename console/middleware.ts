import { NextResponse, type NextRequest } from "next/server";
import { evaluateGate } from "./lib/middleware/gate";
import { resolveLocale } from "./lib/middleware/locale";
import { resolveServerConfig } from "./lib/server/config";
import { LOCALE_COOKIE } from "./i18n/negotiate";

/**
 * Request middleware.
 *
 * Order for page requests: the access gate is evaluated first; an unauthorized
 * page request is redirected without proceeding to locale negotiation. API
 * paths are handled on a dedicated branch and are never locale-rewritten. The
 * locale is never emitted as a URL path segment.
 */
export async function middleware(request: NextRequest): Promise<NextResponse> {
  const config = resolveServerConfig(process.env as Record<string, string | undefined>);
  const pathname = request.nextUrl.pathname;

  const localeResolution = resolveLocale({
    cookieHeader: request.headers.get("cookie") ?? undefined,
    acceptLanguage: request.headers.get("accept-language") ?? undefined,
  });

  const gate = await evaluateGate({
    pathname,
    search: request.nextUrl.search,
    cookieHeader: request.headers.get("cookie") ?? undefined,
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

  // The locale travels as a response cookie/header only; the path is unchanged.
  const response = NextResponse.next({
    request: { headers: request.headers },
  });
  response.headers.set("x-locale", localeResolution.locale);
  response.cookies.set(LOCALE_COOKIE, localeResolution.locale, { path: "/" });
  return response;
}

export const config = {
  matcher: ["/((?!_next/static|_next/image|favicon.ico).*)"],
};