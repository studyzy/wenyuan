import { withBasePath } from "./base-path";

export type TenantStatus = "provisioning" | "active" | "suspended" | "deleted" | string;

export type Tenant = {
  tenant_id: string;
  display_name: string;
  status: TenantStatus;
  /** personal or team. A team is a tenant, so it carries a kind. */
  kind?: string;
  config?: Record<string, unknown>;
  created_at?: string;
  updated_at?: string;
  deleted_at?: string;
};

export type TenantKey = {
  key_id: string;
  prefix: string;
  label: string;
  ephemeral: boolean;
  expires_at?: string;
  revoked_at?: string;
  last_used_at?: string;
  created_at?: string;
};

export type IssuedKey = { plaintext: string; key: TenantKey };
export type TeamRole = "owner" | "member";

export type Team = {
  tenant_id: string;
  display_name: string;
  status: TenantStatus;
  kind: "team" | string;
  config?: Record<string, unknown>;
  member_count: number;
  project_count: number;
  created_at?: string;
  updated_at?: string;
  deleted_at?: string;
};

/** A team as returned by creation: the record, plus the key it came with. */
export type CreatedTeam = { team: Team; plaintext: string; key: TenantKey };

export type TeamMember = {
  tenant_id: string;
  display_name: string;
  role: TeamRole | string;
  joined_at?: string;
};

export type TeamProject = {
  project_id: string;
  name: string;
  bank_id: string;
  member_count: number;
  created_at?: string;
};

export type ProjectMember = {
  tenant_id: string;
  display_name: string;
  added_at?: string;
};

export type DeletedProject = { project_id: string; deleted_banks: string[] };


async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(withBasePath(path), {
    ...init,
    headers: { "content-type": "application/json", ...(init?.headers ?? {}) },
  });
  const text = await response.text();
  let body: Record<string, unknown> = {};
  try {
    body = text ? (JSON.parse(text) as Record<string, unknown>) : {};
  } catch {
    body = {};
  }
  if (!response.ok) {
    const message = body.message ?? body.details ?? body.error;
    throw new Error(typeof message === "string" ? message : "Request failed (" + response.status + ")");
  }
  return body as T;
}

function teamPath(teamId: string): string {
  return "/api/admin/teams/" + encodeURIComponent(teamId);
}

function projectPath(teamId: string, projectId: string): string {
  return teamPath(teamId) + "/projects/" + encodeURIComponent(projectId);
}

export const adminApi = {
  listTenants(params: { status?: string; kind?: string; q?: string; limit?: number; offset?: number } = {}) {
    const query = new URLSearchParams();
    if (params.status) query.set("status", params.status);
    if (params.kind) query.set("kind", params.kind);
    if (params.q) query.set("q", params.q);
    if (params.limit) query.set("limit", String(params.limit));
    if (params.offset) query.set("offset", String(params.offset));
    const suffix = query.toString();
    return request<{ tenants: Tenant[] }>("/api/admin/tenants" + (suffix ? "?" + suffix : ""));
  },
  createTenant(input: {
    tenant_id: string;
    display_name?: string;
    config?: Record<string, unknown>;
    adopt?: boolean;
  }) {
    return request<Tenant>("/api/admin/tenants", {
      method: "POST",
      body: JSON.stringify(input),
    });
  },
  getTenant(tenantId: string) {
    return request<Tenant>("/api/admin/tenants/" + encodeURIComponent(tenantId));
  },
  updateTenant(
    tenantId: string,
    patch: { display_name?: string; config?: Record<string, unknown>; status?: string },
  ) {
    return request<Tenant>("/api/admin/tenants/" + encodeURIComponent(tenantId), {
      method: "PATCH",
      body: JSON.stringify(patch),
    });
  },
  deleteTenant(tenantId: string) {
    return request<Tenant>("/api/admin/tenants/" + encodeURIComponent(tenantId), {
      method: "DELETE",
    });
  },
  listKeys(tenantId: string) {
    return request<{ keys: TenantKey[] }>(
      "/api/admin/tenants/" + encodeURIComponent(tenantId) + "/keys",
    );
  },
  createKey(
    tenantId: string,
    input: { label?: string; expires_at?: string; ephemeral?: boolean } = {},
  ) {
    return request<IssuedKey>(
      "/api/admin/tenants/" + encodeURIComponent(tenantId) + "/keys",
      { method: "POST", body: JSON.stringify(input) },
    );
  },
  enterTenant(tenantId: string) {
    return request<{ tenant_id: string }>(
      "/api/admin/tenants/" + encodeURIComponent(tenantId) + "/session",
      { method: "POST", body: "{}" },
    );
  },
  purgeTenant(tenantId: string, confirm: string) {
    return request<{ tenant_id: string; purged: boolean }>(
      "/api/admin/tenants/" + encodeURIComponent(tenantId) + "/purge",
      { method: "POST", body: JSON.stringify({ confirm }) },
    );
  },
  migrateTenant(tenantId: string) {
    return request<{ status: string }>(
      "/api/admin/tenants/" + encodeURIComponent(tenantId) + "/migrate",
      { method: "POST", body: "{}" },
    );
  },
  tenantSession() {
    return request<{ tenant_id: string | null }>("/api/admin/session");
  },
  exitTenant() {
    return request<{ tenant_id: null }>("/api/admin/session", { method: "DELETE" });
  },
  revokeKey(tenantId: string, keyId: string) {
    return request<{ status: string }>(
      "/api/admin/tenants/" +
        encodeURIComponent(tenantId) +
        "/keys/" +
        encodeURIComponent(keyId),
      { method: "DELETE" },
    );
  },
  overview() {
    return request<Record<string, number>>("/api/admin/overview");
  },

  // Teams. A team is a tenant of kind "team", so these share the tenant plane
  // and are served beside it.
  listTeams(params: { q?: string; limit?: number; offset?: number } = {}) {
    const query = new URLSearchParams();
    if (params.q) query.set("q", params.q);
    if (params.limit) query.set("limit", String(params.limit));
    if (params.offset) query.set("offset", String(params.offset));
    const suffix = query.toString();
    return request<{ teams: Team[] }>("/api/admin/teams" + (suffix ? "?" + suffix : ""));
  },
  createTeam(input: { tenant_id: string; display_name?: string; config?: Record<string, unknown> }) {
    return request<CreatedTeam>("/api/admin/teams", {
      method: "POST",
      body: JSON.stringify(input),
    });
  },
  getTeam(teamId: string) {
    return request<Team>(teamPath(teamId));
  },
  renameTeam(teamId: string, displayName: string) {
    return request<Team>(teamPath(teamId), {
      method: "PATCH",
      body: JSON.stringify({ display_name: displayName }),
    });
  },
  deleteTeam(teamId: string) {
    return request<Team>(teamPath(teamId), { method: "DELETE" });
  },
  listTeamKeys(teamId: string) {
    return request<{ keys: TenantKey[] }>(teamPath(teamId) + "/keys");
  },
  createTeamKey(teamId: string, input: { label?: string } = {}) {
    return request<IssuedKey>(teamPath(teamId) + "/keys", {
      method: "POST",
      body: JSON.stringify(input),
    });
  },
  revokeTeamKey(teamId: string, keyId: string) {
    return request<{ status: string }>(teamPath(teamId) + "/keys/" + encodeURIComponent(keyId), {
      method: "DELETE",
    });
  },
  listTeamMembers(teamId: string) {
    return request<{ members: TeamMember[] }>(teamPath(teamId) + "/members");
  },
  addTeamMember(teamId: string, tenantId: string, role: TeamRole = "member") {
    return request<{ status: string }>(teamPath(teamId) + "/members/" + encodeURIComponent(tenantId), {
      method: "PUT",
      body: JSON.stringify({ role }),
    });
  },
  removeTeamMember(teamId: string, tenantId: string) {
    return request<{ status: string }>(teamPath(teamId) + "/members/" + encodeURIComponent(tenantId), {
      method: "DELETE",
    });
  },
  listTeamProjects(teamId: string) {
    return request<{ projects: TeamProject[] }>(teamPath(teamId) + "/projects");
  },
  createTeamProject(
    teamId: string,
    input: { project_id: string; name: string; adopt_bank?: boolean },
  ) {
    return request<TeamProject>(teamPath(teamId) + "/projects", {
      method: "POST",
      body: JSON.stringify(input),
    });
  },
  getTeamProject(teamId: string, projectId: string) {
    return request<TeamProject>(projectPath(teamId, projectId));
  },
  /**
   * Deletes a project and every bank it owns. The upstream demands the
   * confirmation, so the two cannot drift apart here.
   */
  deleteTeamProject(teamId: string, projectId: string) {
    return request<DeletedProject>(projectPath(teamId, projectId), {
      method: "DELETE",
      body: JSON.stringify({ confirm: projectId, delete_banks: true }),
    });
  },
  projectBankStatus(teamId: string, projectId: string) {
    return request<{ project_id: string; bank_id: string; exists: boolean }>(
      projectPath(teamId, projectId) + "/adopt-bank",
      { method: "POST", body: "{}" },
    );
  },
  listProjectMembers(teamId: string, projectId: string) {
    return request<{ members: ProjectMember[] }>(projectPath(teamId, projectId) + "/members");
  },
  addProjectMember(teamId: string, projectId: string, tenantId: string) {
    return request<{ status: string }>(
      projectPath(teamId, projectId) + "/members/" + encodeURIComponent(tenantId),
      { method: "PUT", body: "{}" },
    );
  },
  removeProjectMember(teamId: string, projectId: string, tenantId: string) {
    return request<{ status: string }>(
      projectPath(teamId, projectId) + "/members/" + encodeURIComponent(tenantId),
      { method: "DELETE" },
    );
  },
  purgeContribution(teamId: string, projectId: string, tenantId: string) {
    return request<{ status: string; rebuild: boolean }>(
      projectPath(teamId, projectId) + "/purge-contribution",
      { method: "POST", body: JSON.stringify({ tenant_id: tenantId }) },
    );
  },
};
