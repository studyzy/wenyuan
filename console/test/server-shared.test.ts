import { describe, expect, it } from "vitest";
import {
  DEFAULT_DATAPLANE_URL,
  DEFAULT_MAX_UPLOAD_SIZE,
  resolveServerConfig,
} from "../lib/server/config";
import { buildErrorBody, localizeError } from "../lib/server/errors";
import {
  createSessionToken,
  clearSessionCookieAttributes,
  isSecureRequest,
  sessionCookieAttributes,
  verifyAccessKey,
  verifySessionToken,
} from "../lib/server/session";
import { encodeBankId, prefixBasePath, sanitizeReturnTo, stripBasePath } from "../lib/server/paths";
import { guardDownloadPath, UPSTREAM_DOWNLOAD_PREFIX } from "../lib/server/download";
import {
  buildUpstreamUrl,
  callUpstream,
  passthrough,
  upstreamHeaders,
} from "../lib/server/upstream";

const NOW = 1_700_000_000_000;

describe("environment contract", () => {
  it("applies documented defaults when unset", () => {
    const config = resolveServerConfig({});
    expect(config.dataplaneUrl).toBe(DEFAULT_DATAPLANE_URL);
    expect(config.dataplaneKey).toBe("");
    expect(config.basePath).toBe("");
    expect(config.maxUploadSize).toBe(DEFAULT_MAX_UPLOAD_SIZE);
    expect(config.accessKey).toBe("");
  });

  it("honors configured values", () => {
    const config = resolveServerConfig({
      HINDSIGHT_CP_ACCESS_KEY: "secret",
      HINDSIGHT_CP_DATAPLANE_API_URL: "http://api.internal:9000/",
      HINDSIGHT_CP_DATAPLANE_API_KEY: "upstream-key",
      NEXT_PUBLIC_BASE_PATH: "/console/",
      HINDSIGHT_CP_MAX_UPLOAD_SIZE: "1024",
    });
    expect(config.accessKey).toBe("secret");
    expect(config.dataplaneUrl).toBe("http://api.internal:9000");
    expect(config.dataplaneKey).toBe("upstream-key");
    expect(config.basePath).toBe("/console");
    expect(config.maxUploadSize).toBe(1024);
  });

  it("uses the documented environment variable names verbatim", () => {
    const names = [
      "HINDSIGHT_CP_ACCESS_KEY",
      "HINDSIGHT_CP_DATAPLANE_API_URL",
      "HINDSIGHT_CP_DATAPLANE_API_KEY",
      "NEXT_PUBLIC_BASE_PATH",
      "HINDSIGHT_CP_MAX_UPLOAD_SIZE",
    ];
    for (const name of names) expect(name).toMatch(/^[A-Z_]+$/);
  });
});

describe("localized error envelope", () => {
  it("replaces an existing message field and strips the internal key", () => {
    const body = localizeError({
      payload: { message: "raw", errorKey: "api.errors.auth.unauthorized" },
      errorKey: "api.errors.auth.unauthorized",
      locale: "en",
    });
    expect(typeof body.message).toBe("string");
    expect(JSON.stringify(body)).not.toContain("errorKey");
  });

  it("adds an error field when no message field is present", () => {
    const body = localizeError({ errorKey: "api.errors.generic.notFound", locale: "en" });
    expect(typeof body.error).toBe("string");
  });

  it("localizes using the requested locale", () => {
    const en = buildErrorBody("api.errors.generic.unauthorized", "en");
    const ja = buildErrorBody("api.errors.generic.unauthorized", "ja");
    expect(en.error).not.toBe(ja.error);
  });

  it("resolves every route error key in the default catalog", () => {
    const keys = [
      "api.errors.generic.unauthorized",
      "api.errors.validation.bankIdRequired",
      "api.errors.validation.missingRequestData",
      "api.errors.validation.fileRequired",
      "api.errors.validation.operationIdRequired",
      "api.errors.validation.targetBankIdRequired",
      "api.errors.validation.bankAndDirectiveIdRequired",
      "api.errors.validation.bankAndMentalModelIdRequired",
      "api.errors.validation.bankAndModelIdRequired",
      "api.errors.auth.invalidRequestBody",
      "api.errors.banks.clone",
      "api.errors.memories.notFound",
      "api.errors.mentalModels.notFound",
      "api.errors.generic.unsupportedIndividualMemoryDelete",
    ];
    for (const key of keys) {
      const body = buildErrorBody(key, "en");
      const message = (body.error ?? body.message) as string;
      expect(message, key).not.toBe(key);
      expect(message.length).toBeGreaterThan(0);
    }
  });
});

describe("session tokens", () => {
  it("issues a token that verifies within its lifetime", () => {
    const token = createSessionToken("secret", "console", NOW);
    expect(verifySessionToken("secret", token, NOW)).toBe(true);
  });

  it("rejects a token signed with a different key", () => {
    const token = createSessionToken("secret", "console", NOW);
    expect(verifySessionToken("other", token, NOW)).toBe(false);
  });

  it("rejects a tampered token", () => {
    const token = createSessionToken("secret", "console", NOW);
    expect(verifySessionToken("secret", `${token}x`, NOW)).toBe(false);
  });

  it("rejects an expired token", () => {
    const token = createSessionToken("secret", "console", NOW);
    expect(verifySessionToken("secret", token, NOW + 2 * 24 * 60 * 60 * 1000)).toBe(false);
  });

  it("compares the access key in constant time", () => {
    expect(verifyAccessKey("secret", "secret")).toBe(true);
    expect(verifyAccessKey("secret", "wrong")).toBe(false);
    expect(verifyAccessKey("", "")).toBe(false);
  });
});

describe("session cookie attributes", () => {
  it("marks the cookie secure only for HTTPS-originated requests", () => {
    expect(isSecureRequest("https")).toBe(true);
    expect(isSecureRequest("http")).toBe(false);
    expect(isSecureRequest(undefined)).toBe(false);
  });

  it("always sets httpOnly, sameSite lax, path root, max age one day", () => {
    const attributes = sessionCookieAttributes(false);
    expect(attributes).toContain("HttpOnly");
    expect(attributes).toContain("SameSite=Lax");
    expect(attributes).toContain("Path=/");
    expect(attributes).toContain("Max-Age=86400");
    expect(attributes).not.toContain("Secure");
  });

  it("clears the cookie with max age zero", () => {
    expect(clearSessionCookieAttributes(false)).toContain("Max-Age=0");
  });
});

describe("path and encoding helpers", () => {
  it("percent-encodes bank ids containing colon, slash, percent and space", () => {
    expect(encodeBankId("team:alpha")).toBe("team%3Aalpha");
    expect(encodeBankId("a/b")).toBe("a%2Fb");
    expect(encodeBankId("50%")).toBe("50%25");
    expect(encodeBankId("a b")).toBe("a%20b");
  });

  it("prefixes and strips the base path", () => {
    expect(prefixBasePath("/console", "/login")).toBe("/console/login");
    expect(prefixBasePath("", "/login")).toBe("/login");
    expect(stripBasePath("/console", "/console/login")).toBe("/login");
    expect(stripBasePath("", "/login")).toBe("/login");
  });

  it("sanitizes unsafe return targets to the dashboard", () => {
    expect(sanitizeReturnTo("https://evil.example")).toBe("/dashboard");
    expect(sanitizeReturnTo("//evil.example")).toBe("/dashboard");
    expect(sanitizeReturnTo("/\\evil")).toBe("/dashboard");
    expect(sanitizeReturnTo("\u0000/banks/acme")).toBe("/banks/acme");
    expect(sanitizeReturnTo("/banks/acme")).toBe("/banks/acme");
    expect(sanitizeReturnTo(undefined)).toBe("/dashboard");
  });
});

describe("download SSRF guard", () => {
  it("allows only the fixed upstream file-download prefix", () => {
    expect(guardDownloadPath(`${UPSTREAM_DOWNLOAD_PREFIX}/file.txt`).allowed).toBe(true);
    expect(guardDownloadPath("/etc/passwd").allowed).toBe(false);
    expect(guardDownloadPath("http://evil.example/x").allowed).toBe(false);
    expect(guardDownloadPath(`${UPSTREAM_DOWNLOAD_PREFIX}/../secrets`).allowed).toBe(false);
    expect(guardDownloadPath(undefined).allowed).toBe(false);
  });
});

describe("upstream client shaping", () => {
  it("builds the upstream URL with a bank id path segment", () => {
    const url = buildUpstreamUrl(
      { dataplaneUrl: "http://127.0.0.1:8000" },
      { path: "/v1/default/banks/team%3Aalpha", query: { limit: "10" } },
    );
    expect(url).toBe("http://127.0.0.1:8000/v1/default/banks/team%3Aalpha?limit=10");
  });

  it("attaches upstream authorization only when a key is configured", () => {
    expect(upstreamHeaders({ dataplaneKey: "" }).authorization).toBeUndefined();
    expect(upstreamHeaders({ dataplaneKey: "k" }).authorization).toBe("Bearer k");
  });
});

describe("upstream response handling", () => {
  const config = {
    accessKey: "",
    dataplaneUrl: "http://upstream.local",
    dataplaneKey: "",
    basePath: "",
    maxUploadSize: 1024,
  };

  it("carries the upstream download filename hint through the passthrough", async () => {
    const original = globalThis.fetch;
    globalThis.fetch = (async () =>
      new Response("bytes", {
        status: 200,
        headers: {
          "content-type": "application/octet-stream",
          "content-disposition": 'attachment; filename="report.txt"',
        },
      })) as typeof fetch;
    try {
      const result = await callUpstream(config, { path: "/v1/default/files/download/a.txt" });
      const response = passthrough(result);
      expect(response.headers.get("content-disposition")).toBe(
        'attachment; filename="report.txt"',
      );
    } finally {
      globalThis.fetch = original;
    }
  });

  it("aborts an upstream request that exceeds the default deadline", async () => {
    const original = globalThis.fetch;
    globalThis.fetch = (async (_input: RequestInfo | URL, init?: RequestInit) =>
      await new Promise<Response>((_resolve, reject) => {
        init?.signal?.addEventListener("abort", () =>
          reject(new DOMException("aborted", "AbortError")),
        );
      })) as typeof fetch;
    try {
      await expect(
        callUpstream(config, {
          path: "/v1/default/banks",
          timeoutMs: 10,
        }),
      ).rejects.toThrow();
    } finally {
      globalThis.fetch = original;
    }
  });
});