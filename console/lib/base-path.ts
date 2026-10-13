/**
 * Browser-facing base-path helper.
 *
 * When the console is mounted under a public base path, asset and link URLs
 * must carry that prefix. Absolute URLs are returned untouched.
 */
import { publicBasePath } from "./client/api";

const ABSOLUTE_URL_PATTERN = /^[a-z][a-z\d+\-.]*:\/\//i;

function ensureLeadingSlash(path: string): string {
  if (!path) return "/";
  return path.startsWith("/") ? path : `/${path}`;
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

  const basePath = publicBasePath();
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