import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { evaluateGate } from "../lib/middleware/gate";
import { isPublicPath, isApiPath, PUBLIC_PATH_PREFIXES } from "../lib/middleware/whitelist";
import { LOCALE_PREFIX_POLICY, resolveLocale, rewriteForLocale } from "../lib/middleware/locale";
import { isStaticAssetPath, MIDDLEWARE_MATCHER, shouldProcess } from "../lib/middleware/matcher";
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
      cookieHeader: `wenyuan_cp_access=${token("secret")}`,
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
      cookieHeader: `wenyuan_cp_access=${tampered}`,
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
    const rewritten = rewriteForLocale("/dashboard", "ja");
    // The rewrite target carries the segment; the address bar never does.
    expect(rewritten.pathname).toBe("/ja/dashboard");
    expect(rewritten.addedLocaleSegment).toBe(true);
  });

  it("does not prefix a path that already carries a locale segment", () => {
    // The middleware re-enters on its own rewrite output, so prefixing again
    // would produce /en/en/dashboard; and a genuine /[locale]/... request must
    // be left for the router to match.
    for (const locale of ["en", "ja", "zh-CN", "yue-Hant"] as const) {
      expect(rewriteForLocale(`/${locale}/dashboard`, locale)).toEqual({
        pathname: `/${locale}/dashboard`,
        addedLocaleSegment: false,
      });
    }
  });

  it("treats a locale-shaped but unsupported first segment as a normal path", () => {
    // `/xx/dashboard` is a path, not a locale; it must still be rewritten to
    // the negotiated locale or it would 404.
    expect(rewriteForLocale("/xx/dashboard", "en")).toEqual({
      pathname: "/en/xx/dashboard",
      addedLocaleSegment: true,
    });
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

  it("excludes the web app manifest from locale rewriting", () => {
    // next-intl rewrites anything it does not recognize as a static asset into
    // the `[locale]` segment; with no locale prefix in `routing` that rewrite
    // resolves back to `/en/manifest.webmanifest`, which is not a route, so the
    // browser installing the app follows a redirect loop instead of reading the
    // manifest. The extension allowlist has to carry `.webmanifest`.
    expect(isStaticAssetPath("/manifest.webmanifest")).toBe(true);
    expect(MIDDLEWARE_MATCHER).toContain("webmanifest");
  });

  it("leaves the BFF API namespace out of the locale rewrite", () => {
    // The route handlers live at `app/api/`, outside `app/[locale]/`, so
    // rewriting them to `/en/api/health` makes every BFF call 404 — and the
    // dashboard renders an error state on its first fetch. The middleware has to
    // return before the rewrite for these.
    const source = readFileSync(join(process.cwd(), "middleware.ts"), "utf8");
    expect(isApiPath("/api/health")).toBe(true);
    expect(isApiPath("/api")).toBe(true);
    expect(source).toContain("isApiPath(pathname)");
    // And the API branch must come before the rewrite, or it is dead.
    expect(source.indexOf("isApiPath(pathname)")).toBeLessThan(
      source.indexOf("rewriteForLocale(pathname"),
    );
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

describe("access gate — role separation", () => {
  const accessKey = "secret";
  const operatorCookie = `wenyuan_cp_access=${token(accessKey)}`;
  const tenantCookie = `wenyuan_cp_access=${createSessionToken(accessKey, "tenant:acme", NOW)}`;

  function gate(pathname: string, cookieHeader: string | undefined) {
    return evaluateGate({
      pathname,
      search: "",
      cookieHeader,
      accessKey,
      basePath: "",
      nowMs: NOW,
    });
  }

  it("lets an operator session reach the management plane", async () => {
    await expect(gate("/api/admin/tenants", operatorCookie)).resolves.toMatchObject({
      kind: "allow",
    });
  });

  it("answers 403 when a tenant session calls the management plane", async () => {
    // A tenant key must not be able to create, purge or re-key other tenants
    // just because it can open the console.
    const decision = await gate("/api/admin/tenants", tenantCookie);
    expect(decision.kind).toBe("api-forbidden");
    if (decision.kind === "api-forbidden") {
      expect(JSON.stringify(decision.body)).not.toContain("api.errors");
    }
  });

  it("still lets a tenant session report and leave itself", async () => {
    await expect(gate("/api/admin/session", tenantCookie)).resolves.toMatchObject({
      kind: "allow",
    });
  });

  it("keeps the data plane open to both roles", async () => {
    await expect(gate("/api/banks", tenantCookie)).resolves.toMatchObject({ kind: "allow" });
    await expect(gate("/api/banks", operatorCookie)).resolves.toMatchObject({ kind: "allow" });
  });
});
