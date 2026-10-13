/**
 * App-relative path helpers.
 *
 * The public base path is a deployment mount point. URL helpers prefix
 * app-relative paths with it and strip it from incoming pathnames.
 */
export function prefixBasePath(basePath: string, path: string): string {
  const prefix = normalizeBasePath(basePath);
  const normalized = path.startsWith("/") ? path : `/${path}`;
  if (!prefix) return normalized;
  if (normalized === "/") return prefix;
  return `${prefix}${normalized}`;
}

export function stripBasePath(basePath: string, pathname: string): string {
  const prefix = normalizeBasePath(basePath);
  if (!prefix) return pathname;
  if (pathname === prefix) return "/";
  if (pathname.startsWith(`${prefix}/`)) return pathname.slice(prefix.length);
  return pathname;
}

export function normalizeBasePath(basePath: string | undefined | null): string {
  const raw = (basePath ?? "").trim();
  if (!raw || raw === "/") return "";
  const withLeading = raw.startsWith("/") ? raw : `/${raw}`;
  return withLeading.replace(/\/+$/, "");
}

/**
 * Percent-encode a bank id for use in a URL path segment. Encodings preserve
 * colon, slash, percent and space as escapes.
 */
export function encodeBankId(bankId: string): string {
  return encodeURIComponent(bankId);
}

/**
 * Validate and sanitize a post-authentication return target. Unsafe values fall
 * back to the dashboard path.
 */
export function sanitizeReturnTo(value: string | undefined | null): string {
  const fallback = "/dashboard";
  if (!value) return fallback;
  const stripped = value.replace(/^[\u0000-\u001f\u007f]+/, "");
  if (!stripped.startsWith("/")) return fallback;
  if (stripped.startsWith("//") || stripped.startsWith("/\\")) return fallback;
  try {
    // A parsed absolute URL would carry a scheme; reject if it has one.
    const parsed = new URL(stripped);
    if (parsed.protocol) return fallback;
  } catch {
    // Not an absolute URL: the app-relative value is acceptable.
  }
  return stripped;
}