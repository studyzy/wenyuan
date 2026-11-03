// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

function translate(key: string) {
  return key;
}

vi.mock("next-intl", () => ({
  useTranslations: () => translate,
}));

const refresh = vi.fn();
vi.mock("next/navigation", () => ({
  useRouter: () => ({ refresh }),
  usePathname: () => "/teams/new",
}));

vi.mock("@/components/console-nav", () => ({
  ConsoleNav: () => null,
}));

const createTeam = vi.fn();
vi.mock("@/lib/admin-api", () => ({
  adminApi: {
    createTeam: (...args: unknown[]) => createTeam(...args),
  },
}));

const { TeamCreateForm } = await import("@/components/team-create-form");

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("TeamCreateForm", () => {
  it("accepts an upper-case id and submits a Chinese display name", async () => {
    createTeam.mockResolvedValue({
      team: { tenant_id: "AcmeTeam", display_name: "研发团队" },
      plaintext: "wyk_test",
      key: {},
    });
    render(<TeamCreateForm />);

    fireEvent.change(screen.getByPlaceholderText("idPlaceholder"), {
      target: { value: "AcmeTeam" },
    });
    fireEvent.change(screen.getByPlaceholderText("displayNamePlaceholder"), {
      target: { value: "研发团队" },
    });
    // The hint says the display name is free text, so an operator knows a
    // Chinese name is expected to work rather than an id-shaped slug.
    expect(screen.getByText("displayNameHint")).toBeDefined();

    fireEvent.submit(screen.getByRole("button", { name: "submit" }).closest("form")!);

    await waitFor(() =>
      expect(createTeam).toHaveBeenCalledWith({
        tenant_id: "AcmeTeam",
        display_name: "研发团队",
      }),
    );
  });

  it("still refuses a Chinese id, which has to name a PostgreSQL schema", () => {
    createTeam.mockResolvedValue({});
    render(<TeamCreateForm />);

    fireEvent.change(screen.getByPlaceholderText("idPlaceholder"), {
      target: { value: "研发团队" },
    });
    fireEvent.submit(screen.getByRole("button", { name: "submit" }).closest("form")!);

    expect(createTeam).not.toHaveBeenCalled();
    expect(screen.getByText("invalidId")).toBeDefined();
  });
});