import { describe, expect, it } from "vitest";
import {
  TENANT_SESSION_COOKIE,
  TENANT_SESSION_MAX_AGE_SECONDS,
  createTenantSessionToken,
  readTenantSessionToken,
  tenantSessionCookieAttributes,
  clearTenantSessionCookieAttributes,
} from "../lib/server/session";
import {
  createTenantSessionToken as createEdgeToken,
  readTenantSessionToken as readEdgeToken,
} from "../lib/server/session-edge";

const NOW = 1_700_000_000_000;
const EXP = Math.floor(NOW / 1000) + TENANT_SESSION_MAX_AGE_SECONDS;

describe("tenant session token", () => {
  it("round-trips and never embeds the plaintext", () => {
    const token = createTenantSessionToken("console-secret", "acme", "wyk_tenant_key", NOW);
    expect(token).not.toContain("wyk_tenant_key");
    expect(readTenantSessionToken("console-secret", token, NOW)).toEqual({
      tenant_id: "acme",
      key: "wyk_tenant_key",
      exp: EXP,
    });
  });

  it("rejects a wrong secret, a tamper and an expired token", () => {
    const token = createTenantSessionToken("console-secret", "acme", "wyk_key", NOW);
    expect(readTenantSessionToken("other-secret", token, NOW)).toBeNull();
    const tampered = token.slice(0, -2) + (token.endsWith("AA") ? "BB" : "AA");
    expect(readTenantSessionToken("console-secret", tampered, NOW)).toBeNull();
    expect(readTenantSessionToken("console-secret", token, NOW + (TENANT_SESSION_MAX_AGE_SECONDS + 1) * 1000)).toBeNull();
  });

  it("interoperates with the Edge implementation in both directions", async () => {
    const nodeToken = createTenantSessionToken("console-secret", "acme", "wyk_node", NOW);
    expect(await readEdgeToken("console-secret", nodeToken, NOW)).toEqual({
      tenant_id: "acme",
      key: "wyk_node",
      exp: EXP,
    });

    const edgeToken = await createEdgeToken("console-secret", "acme", "wyk_edge", NOW);
    expect(readTenantSessionToken("console-secret", edgeToken, NOW)).toEqual({
      tenant_id: "acme",
      key: "wyk_edge",
      exp: EXP,
    });
  });

  it("sets an httpOnly, sameSite lax cookie for ten minutes", () => {
    expect(TENANT_SESSION_COOKIE).toBe("wenyuan_cp_tenant");
    const attributes = tenantSessionCookieAttributes(false);
    expect(attributes).toContain("HttpOnly");
    expect(attributes).toContain("SameSite=Lax");
    expect(attributes).toContain("Path=/");
    expect(attributes).toContain("Max-Age=600");
    expect(attributes).not.toContain("Secure");
    expect(tenantSessionCookieAttributes(true)).toContain("Secure");
    expect(clearTenantSessionCookieAttributes(false)).toContain("Max-Age=0");
  });
});
