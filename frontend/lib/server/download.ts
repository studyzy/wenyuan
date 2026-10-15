/**
 * Fixed upstream file-download prefix. The download proxy may only fetch a
 * path that starts with this prefix, so it cannot be used as an arbitrary open
 * proxy (SSRF guard).
 */
export const UPSTREAM_DOWNLOAD_PREFIX = "/v1/default/files/download";

export type DownloadGuardResult =
  | { allowed: true; path: string }
  | { allowed: false };

export function guardDownloadPath(requestedPath: string | undefined | null): DownloadGuardResult {
  if (!requestedPath) return { allowed: false };
  let decoded: string;
  try {
    decoded = decodeURIComponent(requestedPath);
  } catch {
    decoded = requestedPath;
  }
  if (decoded.includes("..")) return { allowed: false };
  if (decoded !== UPSTREAM_DOWNLOAD_PREFIX && !decoded.startsWith(`${UPSTREAM_DOWNLOAD_PREFIX}/`)) {
    return { allowed: false };
  }
  return { allowed: true, path: decoded };
}