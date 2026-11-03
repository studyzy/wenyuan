/**
 * Deterministic UUIDv5 (RFC 4122 §4.3) — the identity behind idempotent retains.
 *
 * The service admits one async operation per operation id, so deriving that id from the payload
 * turns a re-submission of the SAME content into a no-op instead of a second extraction run. The
 * id must be deterministic (no randomUUID) and a valid UUID — the API rejects anything else.
 */
import { createHash } from "node:crypto";

/**
 * Namespace for every id this package derives. Arbitrary but FIXED: changing it re-mints every
 * operation id and silently disables dedup against everything already submitted.
 */
export const WENYUAN_NAMESPACE = "291e23c3-4686-42b3-a7a1-bec77e75390f";

/** RFC 4122 v5 (SHA-1) UUID for `name` within `namespace`. */
export function uuidV5(name: string, namespace: string = WENYUAN_NAMESPACE): string {
  const ns = Buffer.from(namespace.replace(/-/g, ""), "hex");
  if (ns.length !== 16) throw new Error(`invalid UUID namespace: ${namespace}`);
  const bytes = Buffer.from(
    createHash("sha1").update(ns).update(Buffer.from(name, "utf8")).digest().subarray(0, 16)
  );
  bytes[6] = ((bytes[6] ?? 0) & 0x0f) | 0x50; // version 5
  bytes[8] = ((bytes[8] ?? 0) & 0x3f) | 0x80; // RFC 4122 variant
  const hex = bytes.toString("hex");
  return [
    hex.slice(0, 8),
    hex.slice(8, 12),
    hex.slice(12, 16),
    hex.slice(16, 20),
    hex.slice(20),
  ].join("-");
}
