/**
 * Middleware matcher configuration.
 *
 * The matcher excludes framework internals and applies a static-asset extension
 * allowlist so dynamic segments that contain dots are still processed by the
 * middleware.
 */
export const MATCHER_EXCLUDED_PREFIXES: readonly string[] = [
  "/_next/static",
  "/_next/image",
  "/favicon.ico",
];

export const STATIC_ASSET_EXTENSIONS: readonly string[] = [
  ".css",
  ".js",
  ".mjs",
  ".map",
  ".png",
  ".jpg",
  ".jpeg",
  ".gif",
  ".svg",
  ".webp",
  ".ico",
  ".woff",
  ".woff2",
  ".ttf",
  ".eot",
  ".txt",
  ".json",
  ".xml",
  ".webmanifest",
];

export function isStaticAssetPath(pathname: string): boolean {
  const lower = pathname.toLowerCase();
  return STATIC_ASSET_EXTENSIONS.some((ext) => lower.endsWith(ext));
}

/**
 * Decide whether the middleware should process a request path. Framework
 * internals are skipped; everything else, including dotted dynamic segments, is
 * processed unless it is a known static asset.
 */
export function shouldProcess(pathname: string): boolean {
  for (const prefix of MATCHER_EXCLUDED_PREFIXES) {
    if (pathname === prefix || pathname.startsWith(`${prefix}/`)) return false;
  }
  if (isStaticAssetPath(pathname) && pathname.startsWith("/_next/")) return false;
  return true;
}

/** The matcher pattern exported to the framework. */
export const MIDDLEWARE_MATCHER =
  "/((?!_next|_vercel|.*\\.(?:png|jpe?g|gif|svg|webp|ico|css|js|map|woff2?|ttf|eot|txt|xml|json|webmanifest)$).*)";