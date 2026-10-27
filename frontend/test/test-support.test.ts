import { describe, expect, it } from "vitest";
import {
  catalogFileNames,
  configFixture,
  loadCatalog,
  makeRequest,
  withEnv,
} from "./support";

describe("test support helpers", () => {
  it("loads a catalog by locale", () => {
    const catalog = loadCatalog("en");
    expect(catalog).toHaveProperty("common");
  });

  it("lists one catalog file per declared locale", () => {
    expect(catalogFileNames()).toHaveLength(10);
  });

  it("builds a request with injected headers", () => {
    const request = makeRequest("http://localhost/api/banks", {
      cookie: "NEXT_LOCALE=fr",
      acceptLanguage: "fr-FR",
    });
    expect(request.headers.get("cookie")).toBe("NEXT_LOCALE=fr");
    expect(request.headers.get("accept-language")).toBe("fr-FR");
  });

  it("applies and restores environment overrides", async () => {
    const key = "WENYUAN_CP_ACCESS_KEY";
    const before = process.env[key];
    await withEnv({ [key]: "secret" }, () => {
      expect(process.env[key]).toBe("secret");
    });
    expect(process.env[key]).toBe(before);
  });

  it("provides a default config fixture", () => {
    expect(configFixture().dataplaneUrl).toBe("http://127.0.0.1:8000");
  });
});