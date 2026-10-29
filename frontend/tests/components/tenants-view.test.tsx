// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

function translate(key: string) {
  return key;
}

vi.mock("next-intl", () => ({
  useTranslations: () => translate,
}));

const push = vi.fn();
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push }),
  usePathname: () => "/tenants",
}));

const listTenants = vi.fn();
const getTenant = vi.fn();
const listKeys = vi.fn();
const updateTenant = vi.fn();
const deleteTenant = vi.fn();
const createKey = vi.fn();
const revokeKey = vi.fn();
const tenantSession = vi.fn();
const enterTenant = vi.fn();
const exitTenant = vi.fn();
const migrateTenant = vi.fn();
const purgeTenant = vi.fn();

vi.mock("@/lib/admin-api", () => ({
  adminApi: {
    listTenants: (...args: unknown[]) => listTenants(...args),
    getTenant: (...args: unknown[]) => getTenant(...args),
    listKeys: (...args: unknown[]) => listKeys(...args),
    updateTenant: (...args: unknown[]) => updateTenant(...args),
    deleteTenant: (...args: unknown[]) => deleteTenant(...args),
    createKey: (...args: unknown[]) => createKey(...args),
    revokeKey: (...args: unknown[]) => revokeKey(...args),
    tenantSession: () => tenantSession(),
    enterTenant: (...args: unknown[]) => enterTenant(...args),
    exitTenant: () => exitTenant(),
    migrateTenant: (...args: unknown[]) => migrateTenant(...args),
    purgeTenant: (...args: unknown[]) => purgeTenant(...args),
  },
}));

const { TenantsView } = await import("@/components/tenants-view");
const { TenantDetailView } = await import("@/components/tenant-detail-view");

const tenant = {
  tenant_id: "acme",
  display_name: "Acme",
  status: "active",
  config: { enable_text_search: false },
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

beforeEach(() => {
  vi.clearAllMocks();
  listTenants.mockResolvedValue({ tenants: [tenant] });
  getTenant.mockResolvedValue(tenant);
  listKeys.mockResolvedValue({ keys: [] });
  tenantSession.mockResolvedValue({ tenant_id: null });
});

afterEach(() => cleanup());

describe("tenant console", () => {
  it("lists tenants with their status", async () => {
    render(<TenantsView />);
    expect(await screen.findByText("acme")).toBeTruthy();
    expect(screen.getAllByText("list.statuses.active").length).toBeGreaterThan(0);
    expect(screen.getByText("Acme")).toBeTruthy();
  });

  it("suspends an active tenant", async () => {
    updateTenant.mockResolvedValue({ ...tenant, status: "suspended" });
    render(<TenantDetailView tenantId="acme" />);
    fireEvent.click(await screen.findByText("suspend"));
    await waitFor(() => expect(updateTenant).toHaveBeenCalledWith("acme", { status: "suspended" }));
  });

  it("migrates a tenant and shows the result", async () => {
    migrateTenant.mockResolvedValue({ status: "migrated" });
    render(<TenantDetailView tenantId="acme" />);
    fireEvent.click(await screen.findByText("migrate"));
    await waitFor(() => expect(migrateTenant).toHaveBeenCalledWith("acme"));
    expect(await screen.findByText("migrated")).toBeTruthy();
  });

  it("enters a tenant from the detail page", async () => {
    enterTenant.mockResolvedValue({ tenant_id: "acme" });
    render(<TenantDetailView tenantId="acme" />);
    fireEvent.click(await screen.findByText("enter"));
    await waitFor(() => expect(enterTenant).toHaveBeenCalledWith("acme"));
    await waitFor(() => expect(push).toHaveBeenCalledWith("/dashboard"));
  });

  it("shows a new key once and never in the key list", async () => {
    createKey.mockResolvedValue({
      plaintext: "wyk_secret",
      key: { key_id: "k1", prefix: "wyk_abcd", label: "ci", ephemeral: false },
    });
    render(<TenantDetailView tenantId="acme" />);
    fireEvent.change(await screen.findByPlaceholderText("keyLabel"), {
      target: { value: "ci" },
    });
    fireEvent.click(screen.getByText("createKey"));
    expect(await screen.findByText("wyk_secret")).toBeTruthy();
    await waitFor(() => expect(createKey).toHaveBeenCalledWith("acme", { label: "ci" }));
  });
});
