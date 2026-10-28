import { createHmac, timingSafeEqual } from "node:crypto";

/**
 * Session token primitives.
 *
 * The token is a signed payload whose signature is derived from the access key,
 * so tokens cannot be forged without it and rotating the key invalidates
 * outstanding sessions. Comparisons are constant-time.
 */
export const SESSION_COOKIE = "wenyuan_cp_access";
export const SESSION_MAX_AGE_SECONDS = 60 * 60 * 24;

type TokenPayload = {
  sub: string;
  iat: number;
  exp: number;
};

function sign(secret: string, data: string): string {
  return createHmac("sha256", secret).update(data).digest("base64url");
}

function constantTimeEquals(a: string, b: string): boolean {
  const bufferA = Buffer.from(a);
  const bufferB = Buffer.from(b);
  if (bufferA.length !== bufferB.length) {
    // Still perform a comparison to keep the timing profile stable.
    timingSafeEqual(bufferA, bufferA);
    return false;
  }
  return timingSafeEqual(bufferA, bufferB);
}

export function createSessionToken(secret: string, subject: string, nowMs: number): string {
  const issuedAt = Math.floor(nowMs / 1000);
  const payload: TokenPayload = {
    sub: subject,
    iat: issuedAt,
    exp: issuedAt + SESSION_MAX_AGE_SECONDS,
  };
  const encoded = Buffer.from(JSON.stringify(payload)).toString("base64url");
  return `${encoded}.${sign(secret, encoded)}`;
}

export function verifySessionToken(
  secret: string,
  token: string | undefined,
  nowMs: number,
): boolean {
  if (!secret || !token) return false;
  const dot = token.lastIndexOf(".");
  if (dot <= 0) return false;

  const encoded = token.slice(0, dot);
  const providedSignature = token.slice(dot + 1);
  const expectedSignature = sign(secret, encoded);
  if (!constantTimeEquals(providedSignature, expectedSignature)) return false;

  let payload: TokenPayload;
  try {
    payload = JSON.parse(Buffer.from(encoded, "base64url").toString("utf8")) as TokenPayload;
  } catch {
    return false;
  }

  const now = Math.floor(nowMs / 1000);
  if (typeof payload.exp !== "number" || payload.exp <= now) return false;
  return true;
}

/** Constant-time comparison of a submitted access key against the configured one. */
export function verifyAccessKey(configured: string, submitted: string): boolean {
  if (!configured) return false;
  return constantTimeEquals(submitted, configured);
}

/**
 * Determine whether the session cookie should carry the secure flag. Derived
 * from the forwarded-proto header, independent of the runtime environment name.
 */
export function isSecureRequest(forwardedProto: string | undefined | null): boolean {
  if (!forwardedProto) return false;
  const first = forwardedProto.split(",")[0].trim().toLowerCase();
  return first === "https";
}

export function sessionCookieAttributes(secure: boolean): string {
  const parts = [
    "Path=/",
    "HttpOnly",
    "SameSite=Lax",
    `Max-Age=${SESSION_MAX_AGE_SECONDS}`,
  ];
  if (secure) parts.push("Secure");
  return parts.join("; ");
}

export function clearSessionCookieAttributes(secure: boolean): string {
  const parts = ["Path=/", "HttpOnly", "SameSite=Lax", "Max-Age=0"];
  if (secure) parts.push("Secure");
  return parts.join("; ");
}