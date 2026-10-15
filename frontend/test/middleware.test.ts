import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { evaluateGate } from "../lib/middleware/gate";
import { isPublicPath, PUBLIC_PATH_PREFIXES } from "../lib/middleware/whitelist";
import { LOCALE_PREFIX_POLICY, resolveLocale, rewriteForLocale } from "../lib/middleware/locale";
import { MIDDLEWARE_MATCHER, shouldProcess } from "../lib/middleware/matcher";
import { createSessionToken } from "../lib/server/session";

const NOW = 1_700_000_000_000;

function token(accessKey: string): string {
  return createSessionToken(accessKey, "console", NOW);
}

describe("access gate — open when the key is unset", () => {
  it("allows a non-public API path", async () => {
    const decision = await evaluateGate({
      pathname: "/api/banks",
      search: "",
      cookieHeader: undefined,
      accessKey: "",
      basePath: "",
      nowMs: NOW,
    });
    expect(decision.kind).toBe("allow");
  });

  it("allows a non-public page path without redirecting", async () => {
    const decision = await evaluateGate({
      pathname: "/dashboard",
      search: "",
      cookieHeader: undefined,
      accessKey: "",
      basePath: "",
      nowMs: NOW,
    });
    expect(decision.kind).toBe("allow");
  });
});

describe("access gate — enforced when the key is set", () => {
  it("returns a localized 401 for a non-public API path without a cookie", async () => {
    const decision = await evaluateGate({
      pathname: "/api/banks",
      search: "",
      cookieHeader: undefined,
      accessKey: "secret",
      basePath: "",
      nowMs: NOW,
    });
    expect(decision.kind).toBe("api-unauthorized");
    if (decision.kind === "api-unauthorized") {
      expect(JSON.stringify(decision.body)).not.toContain("__errorKey");
      expect(typeof decision.body.error).toBe("string");
    }
  });

  it("redirects a non-public page request to login with returnTo", async () => {
    const decision = await evaluateGate({
      pathname: "/banks/acme",
      search: "?view=data",
      cookieHeader: undefined,
      accessKey: "secret",
      basePath: "",
      nowMs: NOW,
    });
    expect(decision.kind).toBe("redirect-login");
    if (decision.kind === "redirect-login") {
      expect(decision.location).toContain("/login?");
      expect(decision.location).toContain(encodeURIComponent("/banks/acme?view=data"));
    }
  });

  it("authorizes a request carrying a valid session cookie", async () => {
    const decision = await evaluateGate({
      pathname: "/api/banks",
      search: "",
      cookieHeader: `hindsight_cp_access=${token("secret")}`,
      accessKey: "secret",
      basePath: "",
      nowMs: NOW,
    });
    expect(decision.kind).toBe("allow");
  });

  it("treats a tampered token as unauthenticated", async () => {
    const valid = token("secret");
    const tampered = `${valid.slice(0, -2)}xx`;
    const decision = await evaluateGate({
      pathname: "/api/banks",
      search: "",
      cookieHeader: `hindsight_cp_access=${tampered}`,
      accessKey: "secret",
      basePath: "",
      nowMs: NOW,
    });
    expect(decision.kind).toBe("api-unauthorized");
  });
});

describe("public-path whitelist", () => {
  it("exempts each whitelisted prefix", async () => {
    for (const prefix of PUBLIC_PATH_PREFIXES) {
      expect(isPublicPath(prefix)).toBe(true);
    }
    expect(isPublicPath("/api/health")).toBe(true);
    expect(isPublicPath("/api/version")).toBe(true);
    expect(isPublicPath("/api/auth/login")).toBe(true);
    expect(isPublicPath("/login")).toBe(true);
  });

  it("does not exempt non-whitelisted paths", async () => {
    expect(isPublicPath("/api/banks")).toBe(false);
    expect(isPublicPath("/dashboard")).toBe(false);
  });

  it("exempts whitelisted paths even when the gate is enforced", async () => {
    const decision = await evaluateGate({
      pathname: "/api/health",
      search: "",
      cookieHeader: undefined,
      accessKey: "secret",
      basePath: "",
      nowMs: NOW,
    });
    expect(decision.kind).toBe("allow");
  });
});

describe("locale policy — never a path segment", () => {
  it("declares the never prefix policy", async () => {
    expect(LOCALE_PREFIX_POLICY).toBe("never");
  });

  it("resolves the locale without adding a path segment", async () => {
    expect(resolveLocale({ cookieHeader: "NEXT_LOCALE=ja" }).locale).toBe("ja");
    const rewritten = rewriteForLocale("/dashboard");
    expect(rewritten.pathname).toBe("/dashboard");
    expect(rewritten.addedLocaleSegment).toBe(false);
    expect(rewritten.pathname).not.toMatch(/^\/(en|ja|zh-CN)\b/);
  });
});

describe("base path prefix", () => {
  it("prefixes the login redirect while keeping returnTo app-relative", async () => {
    const decision = await evaluateGate({
      pathname: "/dashboard",
      search: "",
      cookieHeader: undefined,
      accessKey: "secret",
      basePath: "/frontend",
      nowMs: NOW,
    });
    expect(decision.kind).toBe("redirect-login");
    if (decision.kind === "redirect-login") {
      expect(decision.location.startsWith("/frontend/login?")).toBe(true);
      expect(decision.location).toContain(encodeURIComponent("/dashboard"));
      expect(decision.location).not.toContain(encodeURIComponent("/frontend/dashboard"));
    }
  });

  it("keeps the whitelist reachable when a base path is mounted", async () => {
    const login = await evaluateGate({
      pathname: "/frontend/api/auth/login",
      search: "",
      cookieHeader: undefined,
      accessKey: "secret",
      basePath: "/frontend",
      nowMs: NOW,
    });
    expect(login.kind).toBe("allow");

    const health = await evaluateGate({
      pathname: "/frontend/api/health",
      search: "",
      cookieHeader: undefined,
      accessKey: "secret",
      basePath: "/frontend",
      nowMs: NOW,
    });
    expect(health.kind).toBe("allow");
  });

  it("still gates non-whitelisted paths mounted under a base path", async () => {
    const decision = await evaluateGate({
      pathname: "/frontend/api/banks",
      search: "",
      cookieHeader: undefined,
      accessKey: "secret",
      basePath: "/frontend",
      nowMs: NOW,
    });
    expect(decision.kind).toBe("api-unauthorized");
  });
});

describe("middleware matcher", () => {
  it("exposes a matcher that excludes framework internals", () => {
    expect(MIDDLEWARE_MATCHER).toContain("_next");
    expect(MIDDLEWARE_MATCHER).toContain("_vercel");
    expect(MIDDLEWARE_MATCHER).toContain("ico");
  });

  it("inlines the exported matcher verbatim in middleware.ts", () => {
    // Next requires the matcher to be a statically analyzable literal in
    // middleware.ts, so it cannot reference the exported constant; this keeps
    // the two copies from drifting. The source escapes the backslash, hence the
    // doubled form being compared against the raw text.
    const source = readFileSync(join(process.cwd(), "middleware.ts"), "utf8");
    expect(source).toContain(MIDDLEWARE_MATCHER.replace(/\\/g, "\\\\"));
  });

  it("processes dotted dynamic segments that are not static assets", () => {
    expect(shouldProcess("/banks/acme.corp")).toBe(true);
    expect(shouldProcess("/api/banks/team%3Aalpha")).toBe(true);
  });
});
describe("auth and locale contract", () => {
  it("uses the contract error namespace for the access-gate rejection", async () => {
    const { evaluateGate } = await import("../lib/middleware/gate");
    const decision = await evaluateGate({
      pathname: "/api/banks",
      search: "",
      cookieHeader: undefined,
      accessKey: "secret",
      basePath: "",
      locale: "en",
    });
    expect(decision.kind).toBe("api-unauthorized");
    if (decision.kind === "api-unauthorized") {
      const serialized = JSON.stringify(decision.body);
      expect(serialized).not.toContain("errorKey");
      expect(typeof decision.body.error).toBe("string");
    }
  });

  it("opens the gate entirely when no access key is configured", async () => {
    const { evaluateGate } = await import("../lib/middleware/gate");
    const decision = await evaluateGate({
      pathname: "/api/banks",
      search: "",
      cookieHeader: undefined,
      accessKey: "",
      basePath: "",
      locale: "en",
    });
    expect(decision.kind).toBe("allow");
  });

  it("whitelists the sign-in path so the gate cannot loop", async () => {
    const { evaluateGate } = await import("../lib/middleware/gate");
    const decision = await evaluateGate({
      pathname: "/login",
      search: "",
      cookieHeader: undefined,
      accessKey: "secret",
      basePath: "",
      locale: "en",
    });
    expect(decision.kind).toBe("allow");
  });
});
