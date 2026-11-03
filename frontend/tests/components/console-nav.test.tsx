// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import catalog from "../../messages/en.json";

vi.mock("next-intl", () => ({
  useTranslations: (namespace: string) => {
    // Resolve through the real English catalog, so a key the nav asks for but
    // the catalog does not define renders as its full path — the way the
    // deployed console shows it — instead of being hidden by a stub.
    const messages = catalog as unknown as Record<string, unknown>;
    return (key: string) => {
      let node: unknown = messages;
      for (const part of (namespace ? namespace + "." + key : key).split(".")) {
        if (typeof node !== "object" || node === null) return namespace + "." + key;
        node = (node as Record<string, unknown>)[part];
      }
      return typeof node === "string" ? node : namespace + "." + key;
    };
  },
}));

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), refresh: vi.fn() }),
  usePathname: () => "/teams",
}));

const tenantSession = vi.fn();

vi.mock("@/lib/admin-api", () => ({
  adminApi: { tenantSession: () => tenantSession(), exitTenant: vi.fn() },
}));

const { ConsoleNav, visibleLinks } = await import("@/components/console-nav");

beforeEach(() => {
  tenantSession.mockResolvedValue({ tenant_id: null, operator: true });
});

afterEach(() => {
  cleanup();
  tenantSession.mockReset();
});

describe("visibleLinks", () => {
  it("hides the management plane from a tenant session", () => {
    // A tenant key opens one tenant's schema and is refused by /api/admin/**, so
    // the tenant, team and overview entries would only lead to a 403.
    expect(visibleLinks(false).map((link) => link.key)).toEqual(["banks"]);
    expect(visibleLinks(true).map((link) => link.key)).toEqual([
      "banks",
      "tenants",
      "teams",
      "overview",
    ]);
  });
});

describe("ConsoleNav", () => {
  it("renders a label, not a raw message key, for every link", async () => {
    render(<ConsoleNav />);
    for (const label of ["Banks", "Tenants", "Teams", "Overview"]) {
      expect(await screen.findByText(label)).toBeDefined();
    }
    // A missing key renders as its path; catch that class of bug by name.
    expect(screen.queryByText(/tenants\.nav\./)).toBeNull();
  });

  it("links to the team console", async () => {
    render(<ConsoleNav />);
    const link = (await screen.findByText("Teams")).closest("a");
    expect(link?.getAttribute("href")).toBe("/teams");
  });

  it("shows only the bank console to a session that is not the operator", async () => {
    tenantSession.mockResolvedValue({ tenant_id: "acme", operator: false });
    render(<ConsoleNav />);
    expect(await screen.findByText("Banks")).toBeDefined();
    expect(screen.queryByText("Tenants")).toBeNull();
    expect(screen.queryByText("Teams")).toBeNull();
    expect(screen.queryByText("Overview")).toBeNull();
  });
});
