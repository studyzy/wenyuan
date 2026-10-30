// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
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

vi.mock("@/lib/admin-api", () => ({
  adminApi: { tenantSession: () => Promise.resolve({ tenant_id: null }), exitTenant: vi.fn() },
}));

const { ConsoleNav } = await import("@/components/console-nav");

afterEach(() => {
  cleanup();
});

describe("ConsoleNav", () => {
  it("renders a label, not a raw message key, for every link", () => {
    render(<ConsoleNav />);
    for (const label of ["Banks", "Tenants", "Teams", "Overview"]) {
      expect(screen.getByText(label)).toBeDefined();
    }
    // A missing key renders as its path; catch that class of bug by name.
    expect(screen.queryByText(/tenants\.nav\./)).toBeNull();
  });

  it("links to the team console", () => {
    render(<ConsoleNav />);
    const link = screen.getByText("Teams").closest("a");
    expect(link?.getAttribute("href")).toBe("/teams");
  });
});
