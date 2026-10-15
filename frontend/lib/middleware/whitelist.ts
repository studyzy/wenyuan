/**
 * Fixed public-path prefix whitelist.
 *
 * Paths matching one of these prefixes bypass the access gate when the access
 * key is set. The set is a fixed prefix list and stable compatibility surface.
 */
export const PUBLIC_PATH_PREFIXES: readonly string[] = [
  "/login",
  "/api/auth",
  "/api/health",
  "/api/version",
  "/logo.svg",
  "/favicon.ico",
  "/_next",
  "/static",
];

export function isPublicPath(pathname: string): boolean {
  for (const prefix of PUBLIC_PATH_PREFIXES) {
    if (pathname === prefix) return true;
    if (pathname.startsWith(`${prefix}/`)) return true;
    // Asset files served directly at root (logo/favicon) match exactly only.
  }
  return false;
}

export function isApiPath(pathname: string): boolean {
  return pathname === "/api" || pathname.startsWith("/api/");
}