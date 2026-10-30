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
  usePathname: () => "/teams",
}));

const listTeams = vi.fn();
const getTeam = vi.fn();
const listTeamMembers = vi.fn();
const listTeamProjects = vi.fn();
const listTenants = vi.fn();
const listProjectMembers = vi.fn();
const listTeamKeys = vi.fn();
const createTeamKey = vi.fn();
const revokeTeamKey = vi.fn();
const createTeamProject = vi.fn();
const deleteTeamProject = vi.fn();
const addTeamMember = vi.fn();
const removeTeamMember = vi.fn();
const addProjectMember = vi.fn();
const removeProjectMember = vi.fn();
const deleteTeam = vi.fn();
const renameTeam = vi.fn();
const tenantSession = vi.fn();

vi.mock("@/lib/admin-api", () => ({
  adminApi: {
    listTeams: (...args: unknown[]) => listTeams(...args),
    getTeam: (...args: unknown[]) => getTeam(...args),
    listTeamMembers: (...args: unknown[]) => listTeamMembers(...args),
    listTeamProjects: (...args: unknown[]) => listTeamProjects(...args),
    listTenants: (...args: unknown[]) => listTenants(...args),
    listProjectMembers: (...args: unknown[]) => listProjectMembers(...args),
    listTeamKeys: (...args: unknown[]) => listTeamKeys(...args),
    createTeamKey: (...args: unknown[]) => createTeamKey(...args),
    revokeTeamKey: (...args: unknown[]) => revokeTeamKey(...args),
    createTeamProject: (...args: unknown[]) => createTeamProject(...args),
    deleteTeamProject: (...args: unknown[]) => deleteTeamProject(...args),
    addTeamMember: (...args: unknown[]) => addTeamMember(...args),
    removeTeamMember: (...args: unknown[]) => removeTeamMember(...args),
    addProjectMember: (...args: unknown[]) => addProjectMember(...args),
    removeProjectMember: (...args: unknown[]) => removeProjectMember(...args),
    deleteTeam: (...args: unknown[]) => deleteTeam(...args),
    renameTeam: (...args: unknown[]) => renameTeam(...args),
    tenantSession: () => tenantSession(),
  },
}));

const { TeamsView } = await import("@/components/teams-view");
const { TeamDetailView } = await import("@/components/team-detail-view");

const team = {
  tenant_id: "acme",
  display_name: "Acme Engineering",
  status: "active",
  kind: "team",
  member_count: 2,
  project_count: 1,
};

const project = {
  project_id: "web",
  name: "Web",
  bank_id: "acme::Web",
  member_count: 1,
  created_at: "2026-01-01T00:00:00Z",
};

/**
 * openProjectsTab selects the projects panel. Radix renders only the active
 * tab's content, so the project rows and their forms exist only once the tab
 * is selected — which is also what an operator does.
 */
function openProjectsTab() {
  const trigger = screen.getByRole("tab", { name: "tabs.projects" });
  fireEvent.mouseDown(trigger);
  fireEvent.click(trigger);
}

beforeEach(() => {
  tenantSession.mockResolvedValue({ tenant_id: null });
  listTenants.mockResolvedValue({ tenants: [{ tenant_id: "bob", display_name: "Bob", status: "active" }] });
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("TeamsView", () => {
  it("lists teams with their member and project counts", async () => {
    listTeams.mockResolvedValue({ teams: [team] });
    render(<TeamsView />);
    await waitFor(() => expect(screen.getByText("Acme Engineering")).toBeDefined());
    expect(screen.getByText("acme")).toBeDefined();
    // The two counts render side by side with the team row.
    expect(screen.getAllByText("2").length).toBeGreaterThan(0);
    expect(screen.getAllByText("1").length).toBeGreaterThan(0);
  });

  it("reports a failed load instead of an empty list", async () => {
    listTeams.mockRejectedValue(new Error("management plane unreachable"));
    render(<TeamsView />);
    await waitFor(() => expect(screen.getByText("management plane unreachable")).toBeDefined());
  });
});

describe("TeamDetailView", () => {
  beforeEach(() => {
    getTeam.mockResolvedValue(team);
    listTeamMembers.mockResolvedValue({
      members: [{ tenant_id: "bob", display_name: "Bob", role: "member", joined_at: "2026-01-01T00:00:00Z" }],
    });
    listTeamProjects.mockResolvedValue({ projects: [project] });
    listProjectMembers.mockResolvedValue({ members: [] });
    listTeamKeys.mockResolvedValue({ keys: [] });
  });

  it("shows the team's members", async () => {
    render(<TeamDetailView teamId="acme" />);
    await waitFor(() => expect(screen.getByText("Acme Engineering")).toBeDefined());
    expect(screen.getByText("bob")).toBeDefined();
  });

  it("adds a member with the chosen role", async () => {
    addTeamMember.mockResolvedValue({ status: "ok" });
    render(<TeamDetailView teamId="acme" />);
    await waitFor(() => expect(screen.getByText("Acme Engineering")).toBeDefined());
    const input = screen.getByPlaceholderText("memberIdPlaceholder") as HTMLInputElement;
    fireEvent.change(input, { target: { value: "alice" } });
    fireEvent.submit(input.closest("form") as HTMLFormElement);
    await waitFor(() => expect(addTeamMember).toHaveBeenCalledWith("acme", "alice", "member"));
  });

  it("creates a project from the form", async () => {
    createTeamProject.mockResolvedValue(project);
    render(<TeamDetailView teamId="acme" />);
    await waitFor(() => expect(screen.getByText("Acme Engineering")).toBeDefined());
    openProjectsTab();
    fireEvent.change(screen.getByPlaceholderText("projectIdPlaceholder"), { target: { value: "web" } });
    fireEvent.change(screen.getByPlaceholderText("projectNamePlaceholder"), { target: { value: "Web" } });
    const submit = screen.getByText("createProject");
    fireEvent.submit(submit.closest("form") as HTMLFormElement);
    await waitFor(() => expect(createTeamProject).toHaveBeenCalledWith("acme", { project_id: "web", name: "Web" }));
  });

  it("refuses a project name carrying a colon before calling the API", async () => {
    render(<TeamDetailView teamId="acme" />);
    await waitFor(() => expect(screen.getByText("Acme Engineering")).toBeDefined());
    openProjectsTab();
    fireEvent.change(screen.getByPlaceholderText("projectIdPlaceholder"), { target: { value: "web2" } });
    fireEvent.change(screen.getByPlaceholderText("projectNamePlaceholder"), { target: { value: "a:b" } });
    fireEvent.submit(screen.getByText("createProject").closest("form") as HTMLFormElement);
    await waitFor(() => expect(screen.getByText("projectNameInvalid")).toBeDefined());
    expect(createTeamProject).not.toHaveBeenCalled();
  });

  it("deletes a project only after the operator types its id, and says how many banks went", async () => {
    deleteTeamProject.mockResolvedValue({ project_id: "web", deleted_banks: ["acme::Web", "acme::Web.docs"] });
    const prompt = vi.spyOn(window, "prompt").mockReturnValue("web");
    render(<TeamDetailView teamId="acme" />);
    await waitFor(() => expect(screen.getByText("Acme Engineering")).toBeDefined());
    openProjectsTab();
    fireEvent.click(screen.getAllByText("delete")[screen.getAllByText("delete").length - 1]);
    await waitFor(() => expect(deleteTeamProject).toHaveBeenCalledWith("acme", "web"));
    await waitFor(() => expect(screen.getByText("projectDeleted")).toBeDefined());
    prompt.mockRestore();
  });

  it("does not delete a project when the typed confirmation is wrong", async () => {
    const prompt = vi.spyOn(window, "prompt").mockReturnValue("wrong");
    render(<TeamDetailView teamId="acme" />);
    await waitFor(() => expect(screen.getByText("Acme Engineering")).toBeDefined());
    openProjectsTab();
    fireEvent.click(screen.getAllByText("delete")[screen.getAllByText("delete").length - 1]);
    expect(deleteTeamProject).not.toHaveBeenCalled();
    prompt.mockRestore();
  });
});
describe("TeamDetailView keys", () => {
  beforeEach(() => {
    getTeam.mockResolvedValue(team);
    listTeamMembers.mockResolvedValue({ members: [] });
    listTeamProjects.mockResolvedValue({ projects: [] });
  });

  it("lists the team's keys and mints a new one once", async () => {
    listTeamKeys.mockResolvedValue({
      keys: [{ key_id: "k1", prefix: "wyk_abcd", label: "ci", ephemeral: false, created_at: "2026-01-01T00:00:00Z" }],
    });
    createTeamKey.mockResolvedValue({
      plaintext: "wyk_plaintext",
      key: { key_id: "k2", prefix: "wyk_wxyz", label: "ci", ephemeral: false },
    });
    render(<TeamDetailView teamId="acme" />);
    await waitFor(() => expect(screen.getByText("Acme Engineering")).toBeDefined());
    const trigger = screen.getByRole("tab", { name: "tabs.keys" });
    fireEvent.mouseDown(trigger);
    fireEvent.click(trigger);
    await waitFor(() => expect(screen.getByText("wyk_abcd")).toBeDefined());

    const input = screen.getByPlaceholderText("keyLabel") as HTMLInputElement;
    fireEvent.change(input, { target: { value: "ci" } });
    fireEvent.submit(input.closest("form") as HTMLFormElement);
    await waitFor(() => expect(createTeamKey).toHaveBeenCalledWith("acme", { label: "ci" }));
    // The plaintext is surfaced exactly once, where the operator can copy it.
    await waitFor(() => expect(screen.getByText("wyk_plaintext")).toBeDefined());
  });
});
