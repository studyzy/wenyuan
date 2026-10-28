/**
 * Edge-runtime session verification.
 *
 * The request middleware runs on the Edge runtime, which does not expose the
 * Node crypto module. This module performs the same HMAC-SHA256 signature check
 * using the Web Crypto API so the middleware can validate sessions without any
 * Node-only dependency. The token format is shared with the route-handler
 * implementation: base64url(payload) + "." + base64url(hmac).
 */

export const SESSION_COOKIE = "wenyuan_cp_access";
export const SESSION_MAX_AGE_SECONDS = 60 * 60 * 24;

type TokenPayload = {
  sub: string;
  iat: number;
  exp: number;
};

function base64UrlDecode(value: string): Uint8Array {
  const normalized = value.replace(/-/g, "+").replace(/_/g, "/");
  const padded = normalized + "=".repeat((4 - (normalized.length % 4)) % 4);
  const binary = atob(padded);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i += 1) {
    bytes[i] = binary.charCodeAt(i);
  }
  return bytes;
}

function base64UrlEncode(bytes: Uint8Array): string {
  let binary = "";
  for (const byte of bytes) {
    binary += String.fromCharCode(byte);
  }
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

async function hmacSha256(secret: string, data: string): Promise<Uint8Array> {
  const key = await crypto.subtle.importKey(
    "raw",
    new TextEncoder().encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );
  const signature = await crypto.subtle.sign("HMAC", key, new TextEncoder().encode(data));
  return new Uint8Array(signature);
}

/** Constant-time byte comparison; length differences compare fixed-length data. */
function constantTimeEquals(a: Uint8Array, b: Uint8Array): boolean {
  const length = Math.max(a.length, b.length);
  let diff = a.length === b.length ? 0 : 1;
  for (let i = 0; i < length; i += 1) {
    diff |= (a[i] ?? 0) ^ (b[i] ?? 0);
  }
  return diff === 0;
}

/** Constant-time comparison of a submitted access key against the configured one. */
export async function verifyAccessKey(configured: string, submitted: string): Promise<boolean> {
  if (!configured) return false;
  const [a, b] = await Promise.all([
    hmacSha256("access-key-compare", submitted),
    hmacSha256("access-key-compare", configured),
  ]);
  return constantTimeEquals(a, b);
}

/**
 * Verify a session token: the signature must match and the payload must not be
 * expired. Returns false for any malformed input rather than throwing.
 */
export async function verifySessionToken(
  secret: string,
  token: string | undefined,
  nowMs: number,
): Promise<boolean> {
  if (!secret || !token) return false;
  const dot = token.lastIndexOf(".");
  if (dot <= 0) return false;

  const encoded = token.slice(0, dot);
  const provided = token.slice(dot + 1);
  const expected = await hmacSha256(secret, encoded);
  if (!constantTimeEquals(base64UrlDecode(provided), expected)) return false;
  if (base64UrlEncode(expected) !== provided) return false;

  let payload: TokenPayload;
  try {
    payload = JSON.parse(new TextDecoder().decode(base64UrlDecode(encoded))) as TokenPayload;
  } catch {
    return false;
  }

  const now = Math.floor(nowMs / 1000);
  if (typeof payload.exp !== "number" || payload.exp <= now) return false;
  return true;
}
