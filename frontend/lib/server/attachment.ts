/**
 * Attachment byte-proxy guard.
 *
 * The attachment proxy may only fetch a content-addressed short id. Accepting
 * anything else would let a caller steer the proxied upstream path (SSRF), so
 * only a fixed-length lowercase hexadecimal value is allowed.
 */
export const ATTACHMENT_ID_LENGTH = 64;
export const ATTACHMENT_ID_PATTERN = new RegExp(`^[0-9a-f]{${ATTACHMENT_ID_LENGTH}}$`);

export function isAttachmentId(value: string | undefined | null): boolean {
  return typeof value === "string" && ATTACHMENT_ID_PATTERN.test(value);
}

/** Generic binary content type used when the upstream omits one. */
export const FALLBACK_BINARY_CONTENT_TYPE = "application/octet-stream";