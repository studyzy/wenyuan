import {
  createCipheriv,
  createDecipheriv,
  createHash,
  createHmac,
  randomBytes,
  timingSafeEqual,
} from "node:crypto";

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
  return readSessionToken(secret, token, nowMs) !== null;
}

/**
 * Read a session token: signature and expiry must hold. Returns the payload, or
 * null for any malformed input. Callers that need the subject (the proxy, to
 * tell an operator session from a tenant one) use this instead of the bare
 * verification.
 */
export function readSessionToken(
  secret: string,
  token: string | undefined,
  nowMs: number,
): TokenPayload | null {
  if (!secret || !token) return null;
  const dot = token.lastIndexOf(".");
  if (dot <= 0) return null;

  const encoded = token.slice(0, dot);
  const providedSignature = token.slice(dot + 1);
  const expectedSignature = sign(secret, encoded);
  if (!constantTimeEquals(providedSignature, expectedSignature)) return null;

  let payload: TokenPayload;
  try {
    payload = JSON.parse(Buffer.from(encoded, "base64url").toString("utf8")) as TokenPayload;
  } catch {
    return null;
  }

  const now = Math.floor(nowMs / 1000);
  if (typeof payload.exp !== "number" || payload.exp <= now) return null;
  return payload;
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
/**
 * Tenant-session token: an AEAD-encrypted short-lived tenant selection.
 *
 * A session entered with a tenant key holds that key so the BFF can proxy the
 * bank pages without ever handing it to browser JavaScript; a session entered
 * by the operator holds only the tenant id and is paired with the operator
 * credential at the proxy. Either way the payload is a bearer secret, so the
 * cookie is encrypted (AES-256-GCM) rather than merely signed; the format is
 * base64url(iv) + "." + base64url(ciphertext || tag), which the Edge
 * implementation reads and writes identically.
 */
export const TENANT_SESSION_COOKIE = "wenyuan_cp_tenant";
export const TENANT_SESSION_MAX_AGE_SECONDS = 10 * 60;

export type TenantSessionPayload = {
  tenant_id: string;
  /** The tenant key; empty when an operator entered the tenant. */
  key: string;
  /**
   * True when the session was entered by the deployment operator. Such a
   * session holds no tenant key: the BFF attaches the operator credential and
   * names the tenant on the data plane.
   */
  admin?: boolean;
  exp: number;
};

function tenantSessionKey(secret: string): Buffer {
  return createHash("sha256").update(secret).digest();
}

function sealTenantSession(secret: string, payload: TenantSessionPayload): string {
  const iv = randomBytes(12);
  const cipher = createCipheriv("aes-256-gcm", tenantSessionKey(secret), iv);
  const ciphertext = Buffer.concat([
    cipher.update(JSON.stringify(payload), "utf8"),
    cipher.final(),
  ]);
  const sealed = Buffer.concat([ciphertext, cipher.getAuthTag()]);
  return iv.toString("base64url") + "." + sealed.toString("base64url");
}

export function createTenantSessionToken(
  secret: string,
  tenantId: string,
  key: string,
  nowMs: number,
): string {
  return sealTenantSession(secret, {
    tenant_id: tenantId,
    key,
    exp: Math.floor(nowMs / 1000) + TENANT_SESSION_MAX_AGE_SECONDS,
  });
}

/**
 * A tenant entered by the operator. The cookie names the tenant and nothing
 * else: the data-plane credential is the operator key the BFF already holds,
 * attached only when it proxies a request for this session.
 */
export function createAdminTenantSessionToken(
  secret: string,
  tenantId: string,
  nowMs: number,
): string {
  return sealTenantSession(secret, {
    tenant_id: tenantId,
    key: "",
    admin: true,
    exp: Math.floor(nowMs / 1000) + TENANT_SESSION_MAX_AGE_SECONDS,
  });
}

export function readTenantSessionToken(
  secret: string,
  token: string | undefined,
  nowMs: number,
): TenantSessionPayload | null {
  if (!secret || !token) return null;
  const parts = token.split(".");
  if (parts.length !== 2) return null;
  try {
    const iv = Buffer.from(parts[0], "base64url");
    const sealed = Buffer.from(parts[1], "base64url");
    if (iv.length !== 12 || sealed.length <= 16) return null;
    const decipher = createDecipheriv("aes-256-gcm", tenantSessionKey(secret), iv);
    decipher.setAuthTag(sealed.subarray(sealed.length - 16));
    const plaintext = Buffer.concat([
      decipher.update(sealed.subarray(0, sealed.length - 16)),
      decipher.final(),
    ]).toString("utf8");
    const payload = JSON.parse(plaintext) as TenantSessionPayload;
    if (
      typeof payload.tenant_id !== "string" ||
      typeof payload.key !== "string" ||
      typeof payload.exp !== "number" ||
      (payload.admin !== undefined && typeof payload.admin !== "boolean")
    ) {
      return null;
    }
    // An operator session names a tenant but carries no key; a tenant-key
    // session must carry one. Anything else is malformed rather than a
    // session that silently falls back to a credential it does not have.
    if (payload.admin === true ? payload.key !== "" : payload.key === "") return null;
    if (payload.exp * 1000 <= nowMs) return null;
    return payload;
  } catch {
    return null;
  }
}

export function tenantSessionCookieAttributes(secure: boolean): string {
  const parts = [
    "Path=/",
    "HttpOnly",
    "SameSite=Lax",
    `Max-Age=${TENANT_SESSION_MAX_AGE_SECONDS}`,
  ];
  if (secure) parts.push("Secure");
  return parts.join("; ");
}

export function clearTenantSessionCookieAttributes(secure: boolean): string {
  const parts = ["Path=/", "HttpOnly", "SameSite=Lax", "Max-Age=0"];
  if (secure) parts.push("Secure");
  return parts.join("; ");
}
