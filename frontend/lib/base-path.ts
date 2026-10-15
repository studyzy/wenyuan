/**
 * Browser-facing base-path helper.
 *
 * When the frontend is mounted under a public base path, asset and link URLs
 * must carry that prefix. Absolute URLs are returned untouched.
 */
const ABSOLUTE_URL_PATTERN = /^[a-z][a-z\d+\-.]*:\/\//i;

/** Collapse a configured base path to `""` or a leading-slash, un-trailing form. */
export function normalizeBasePath(value = process.env.NEXT_PUBLIC_BASE_PATH): string {
  if (!value || value === "/") return "";
  const trimmed = value.trim();
  const withLeadingSlash = trimmed.startsWith("/") ? trimmed : `/${trimmed}`;
  return withLeadingSlash.replace(/\/+$/, "");
}

function ensureLeadingSlash(path: string): string {
  if (!path) return "/";
  return path.startsWith("/") ? path : `/${path}`;
}

/**
 * Remove the base path from an app path, so a pathname that arrived with the
 * deployment mount point attached can be matched against app-relative routes.
 * Absolute URLs, protocol-relative URLs, and paths that never carried the
 * prefix are returned unchanged; a query or fragment survives the strip.
 */
export function stripBasePath(path: string): string {
  if (ABSOLUTE_URL_PATTERN.test(path) || path.startsWith("//")) {
    return path;
  }

  const basePath = normalizeBasePath();
  const normalizedPath = ensureLeadingSlash(path);

  if (!basePath) return normalizedPath;

  const match = normalizedPath.match(/^([^?#]*)(.*)$/);
  const pathname = match?.[1] || "/";
  const suffix = match?.[2] || "";

  if (pathname === basePath) return `/${suffix}`;
  if (pathname.startsWith(`${basePath}/`)) return `${pathname.slice(basePath.length)}${suffix}`;

  return normalizedPath;
}

/**
 * Validate and sanitize a post-authentication `returnTo` target, rejecting
 * open-redirect vectors (`//evil.com`, `javascript:`, leading controls the URL
 * parser ignores) and returning a same-origin app path with the deployment's
 * base path stripped, so client-side `router.push()` does not double-prefix.
 * Unsafe values fall back to the dashboard.
 */
export function sanitizeReturnTo(
  rawValue: string | null | undefined,
  fallback = "/dashboard",
): string {
  if (!rawValue) return fallback;
  const trimmed = rawValue.replace(/^[\x00-\x20]+/, "");
  if (!trimmed.startsWith("/")) return fallback;
  if (trimmed.startsWith("//") || trimmed.startsWith("/\\") || ABSOLUTE_URL_PATTERN.test(trimmed)) {
    return fallback;
  }
  return stripBasePath(trimmed);
}

/**
 * Prefix a path with the configured base path when one is set. Absolute URLs
 * and protocol-relative URLs pass through unchanged, and an already-prefixed
 * path is not prefixed twice.
 */
export function withBasePath(path: string): string {
  if (ABSOLUTE_URL_PATTERN.test(path) || path.startsWith("//")) {
    return path;
  }

  const basePath = normalizeBasePath();
  const normalizedPath = ensureLeadingSlash(path);

  if (!basePath) return normalizedPath;
  if (
    normalizedPath === basePath ||
    normalizedPath.startsWith(`${basePath}/`) ||
    normalizedPath.startsWith(`${basePath}?`)
  ) {
    return normalizedPath;
  }

  return `${basePath}${normalizedPath}`;
}