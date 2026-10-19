/**
 * Fixed upstream file-download prefix. The download proxy may only fetch a
 * path that starts with this prefix, so it cannot be used as an arbitrary open
 * proxy (SSRF guard).
 */
export const UPSTREAM_DOWNLOAD_PREFIX = "/v1/default/files/download";

export type DownloadGuardResult =
  | { allowed: true; path: string }
  | { allowed: false };

/**
 * The guard validates the decoded form and forwards the path exactly as it
 * arrived. A stored-file key is opaque and may itself carry percent-escapes —
 * a bank identifier the key encodes — and the upstream splits the path it
 * receives rather than decoding it, so handing upstream the decoded form would
 * address a key nothing is stored under. The decoded form is only what the
 * prefix and traversal checks read.
 */
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
  return { allowed: true, path: requestedPath };
}