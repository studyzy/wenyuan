// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

function translate(key: string, values?: Record<string, unknown>) {
  return values ? key + ":" + JSON.stringify(values) : key;
}

vi.mock("next-intl", () => ({
  useTranslations: () => translate,
}));

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), refresh: vi.fn() }),
  usePathname: () => "/tenants/acme",
}));

const getTenant = vi.fn();
const listKeys = vi.fn();
const getTenantAggregate = vi.fn();
const resetTenantAggregate = vi.fn();
const rebuildTenantAggregate = vi.fn();

vi.mock("@/lib/admin-api", () => ({
  adminApi: {
    getTenant: (...args: unknown[]) => getTenant(...args),
    listKeys: (...args: unknown[]) => listKeys(...args),
    getTenantAggregate: (...args: unknown[]) => getTenantAggregate(...args),
    resetTenantAggregate: (...args: unknown[]) => resetTenantAggregate(...args),
    rebuildTenantAggregate: (...args: unknown[]) => rebuildTenantAggregate(...args),
    updateTenant: vi.fn(),
    deleteTenant: vi.fn(),
    createKey: vi.fn(),
    revokeKey: vi.fn(),
    tenantSession: vi.fn().mockResolvedValue({ tenant_id: null }),
    enterTenant: vi.fn(),
    migrateTenant: vi.fn(),
    purgeTenant: vi.fn(),
  },
}));

const { TenantDetailView } = await import("@/components/tenant-detail-view");

const tenant = {
  tenant_id: "acme",
  display_name: "Acme",
  status: "active",
  kind: "personal",
  config: {},
};

beforeEach(() => {
  vi.clearAllMocks();
  getTenant.mockResolvedValue(tenant);
  listKeys.mockResolvedValue({ keys: [] });
});

afterEach(() => cleanup());

describe("personal aggregate panel", () => {
  it("shows a person's aggregate bank and counts", async () => {
    getTenantAggregate.mockResolvedValue({
      tenant_id: "acme",
      kind: "personal",
      aggregate: true,
      bank_id: "acme::myself",
      aggregate_facts: 12,
      source_banks: 4,
      links: 6,
      topics: 9,
      halted: false,
      last_round_at: "2026-10-31T00:00:00Z",
    });
    render(<TenantDetailView tenantId="acme" />);
    expect(await screen.findByText("aggregate")).toBeTruthy();
    expect(screen.getByText("acme::myself")).toBeTruthy();
    expect(screen.getByText(/aggregateFacts:/)).toBeTruthy();
  });

  it("says so when rows were written straight into the aggregate bank", async () => {
    getTenantAggregate.mockResolvedValue({
      tenant_id: "acme",
      aggregate: true,
      bank_id: "acme::myself",
      aggregate_facts: 1,
      source_banks: 1,
      user_facts: 3,
      halted: false,
    });
    render(<TenantDetailView tenantId="acme" />);
    expect(await screen.findByText(/aggregateUserFacts:/)).toBeTruthy();
  });

  it("only offers the clear-the-stop action while the aggregate is halted", async () => {
    getTenantAggregate.mockResolvedValue({
      tenant_id: "acme",
      aggregate: true,
      bank_id: "acme::myself",
      aggregate_facts: 0,
      source_banks: 0,
      halted: false,
    });
    render(<TenantDetailView tenantId="acme" />);
    const reset = (await screen.findByText("aggregateResetAction")) as HTMLButtonElement;
    expect(reset.disabled).toBe(true);
  });

  it("resets a halted aggregate", async () => {
    getTenantAggregate.mockResolvedValue({
      tenant_id: "acme",
      aggregate: true,
      bank_id: "acme::myself",
      aggregate_facts: 0,
      source_banks: 0,
      halted: true,
      last_error: "model down",
    });
    resetTenantAggregate.mockResolvedValue({ status: "reset" });
    render(<TenantDetailView tenantId="acme" />);
    fireEvent.click(await screen.findByText("aggregateResetAction"));
    await waitFor(() => expect(resetTenantAggregate).toHaveBeenCalledWith("acme"));
    expect(await screen.findByText("aggregateReset")).toBeTruthy();
  });

  it("asks for a rebuild", async () => {
    getTenantAggregate.mockResolvedValue({
      tenant_id: "acme",
      aggregate: true,
      bank_id: "acme::myself",
      aggregate_facts: 2,
      source_banks: 2,
      halted: false,
    });
    rebuildTenantAggregate.mockResolvedValue({ status: "rebuild_requested" });
    render(<TenantDetailView tenantId="acme" />);
    fireEvent.click(await screen.findByText("aggregateRebuildAction"));
    await waitFor(() => expect(rebuildTenantAggregate).toHaveBeenCalledWith("acme"));
    expect(await screen.findByText("aggregateRebuild")).toBeTruthy();
  });

  it("lists a team's members instead of a team aggregate", async () => {
    getTenantAggregate.mockResolvedValue({
      tenant_id: "acme",
      kind: "team",
      aggregate: false,
      reason: "a team tenant takes no aggregate of its own",
      members: [
        { tenant_id: "alice", bank_id: "alice::myself", aggregate_facts: 5 },
        { tenant_id: "bob", bank_id: "bob::myself", aggregate_facts: 2 },
      ],
    });
    render(<TenantDetailView tenantId="acme" />);
    expect(await screen.findByText("a team tenant takes no aggregate of its own")).toBeTruthy();
    expect(screen.getByText("alice")).toBeTruthy();
    expect(screen.getByText("bob::myself")).toBeTruthy();
  });

  it("keeps the tenant page when the aggregate surface is unavailable", async () => {
    getTenantAggregate.mockRejectedValue(new Error("not configured"));
    render(<TenantDetailView tenantId="acme" />);
    // The page is still the tenant's: its schema and its keys sections render,
    // and no aggregate panel is offered on a deployment that cannot answer.
    expect(await screen.findByText("keys")).toBeTruthy();
    expect(screen.queryByText("aggregate")).toBeNull();
  });
});
