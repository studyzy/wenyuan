import { afterEach, describe, expect, it } from "vitest";
import { apiUrl, publicBasePath } from "../lib/client/api";
import { clearBankSelection, createBankSelection, readFeatureFlags, selectBank } from "../lib/client/state";
import { withEnv } from "./support/fixtures";

afterEach(() => {
  delete process.env.NEXT_PUBLIC_BASE_PATH;
});

describe("client base-path prefixing", () => {
  it("prefixes browser requests when a base path is configured", async () => {
    await withEnv({ NEXT_PUBLIC_BASE_PATH: "/console" }, () => {
      expect(publicBasePath()).toBe("/console");
      expect(apiUrl("/api/banks")).toBe("/console/api/banks");
    });
  });

  it("leaves requests unprefixed when no base path is configured", async () => {
    await withEnv({ NEXT_PUBLIC_BASE_PATH: undefined }, () => {
      expect(publicBasePath()).toBe("");
      expect(apiUrl("/api/banks")).toBe("/api/banks");
    });
  });

  it("serializes query parameters", () => {
    expect(apiUrl("/api/list", { bank_id: "acme" })).toBe("/api/list?bank_id=acme");
  });
});

describe("bank selection state", () => {
  it("selects and clears the current bank", () => {
    const initial = createBankSelection();
    expect(initial.bankId).toBeNull();
    const selected = selectBank(initial, "acme");
    expect(selected.bankId).toBe("acme");
    expect(clearBankSelection(selected).bankId).toBeNull();
  });
});

describe("feature flags", () => {
  it("reads the access-key-auth flag from the version payload", () => {
    expect(readFeatureFlags({ features: { access_key_auth: true } }).accessKeyAuth).toBe(true);
    expect(readFeatureFlags({ features: {} }).accessKeyAuth).toBe(false);
  });
});