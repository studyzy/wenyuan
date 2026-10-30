import { describe, expect, it } from "vitest";
import { GET as listTeams, POST as createTeam } from "../app/api/admin/teams/route";
import {
  DELETE as deleteTeam,
  GET as getTeam,
  PATCH as patchTeam,
} from "../app/api/admin/teams/[teamId]/route";
import { GET as listTeamMembers } from "../app/api/admin/teams/[teamId]/members/route";
import { DELETE as removeTeamMember, PUT as addTeamMember } from "../app/api/admin/teams/[teamId]/members/[tenantId]/route";
import {
  GET as listTeamProjects,
  POST as createTeamProject,
} from "../app/api/admin/teams/[teamId]/projects/route";
import { DELETE as deleteProject, GET as getProject } from "../app/api/admin/teams/[teamId]/projects/[projectId]/route";
import { GET as listProjectMembers } from "../app/api/admin/teams/[teamId]/projects/[projectId]/members/route";
import {
  DELETE as removeProjectMember,
  PUT as addProjectMember,
} from "../app/api/admin/teams/[teamId]/projects/[projectId]/members/[tenantId]/route";
import { POST as purgeContribution } from "../app/api/admin/teams/[teamId]/projects/[projectId]/purge-contribution/route";
import { stubFetch, withEnv } from "./support/fixtures";
import { makeRequest, readJson } from "./support/harness";

const ADMIN_ENV = {
  WENYUAN_CP_ADMIN_API_URL: "http://admin.internal:18890",
  WENYUAN_CP_ADMIN_API_KEY: "operator-key",
  WENYUAN_CP_DATAPLANE_API_KEY: "tenant-key",
};

const teamParams = (teamId: string) => ({ params: Promise.resolve({ teamId }) });
const projectParams = (teamId: string, projectId: string) => ({
  params: Promise.resolve({ teamId, projectId }),
});
const memberParams = (teamId: string, tenantId: string) => ({
  params: Promise.resolve({ teamId, tenantId }),
});
const projectMemberParams = (teamId: string, projectId: string, tenantId: string) => ({
  params: Promise.resolve({ teamId, projectId, tenantId }),
});

async function runWithStub(
  responses: Array<{ status?: number; body?: unknown }>,
  run: (stub: ReturnType<typeof stubFetch>) => Promise<void>,
): Promise<void> {
  await withEnv(ADMIN_ENV, async () => {
    const stub = stubFetch(responses);
    try {
      await run(stub);
    } finally {
      stub.restore();
    }
  });
}

describe("team admin BFF", () => {
  it("forwards the team list with its filters and the operator key", async () => {
    await runWithStub([{ body: { teams: [{ tenant_id: "acme", kind: "team" }] } }], async (stub) => {
      const response = await listTeams(makeRequest("http://localhost/api/admin/teams?q=ac&limit=10"));
      expect(response.status).toBe(200);
      expect(stub.calls[0]).toBe("http://admin.internal:18890/admin/v1/teams?q=ac&limit=10");
      const headers = new Headers(stub.headers[0] as HeadersInit);
      expect(headers.get("authorization")).toBe("Bearer operator-key");
      // The management plane is called with the operator key, never with the
      // data plane's tenant key, even though the environment carries both.
      expect(headers.get("x-api-key")).toBe("operator-key");
      expect(await readJson(response)).toEqual({ teams: [{ tenant_id: "acme", kind: "team" }] });
    });
  });

  it("creates a team with the upstream 201 and hands back its key", async () => {
    await runWithStub(
      [{ status: 201, body: { team: { tenant_id: "acme", kind: "team" }, plaintext: "wyk_abc" } }],
      async (stub) => {
        const response = await createTeam(
          makeRequest("http://localhost/api/admin/teams", {
            method: "POST",
            body: JSON.stringify({ tenant_id: "acme" }),
          }),
        );
        expect(response.status).toBe(201);
        expect(stub.calls[0]).toBe("http://admin.internal:18890/admin/v1/teams");
        expect(stub.methods[0]).toBe("POST");
        expect(stub.bodies[0]).toBe(JSON.stringify({ tenant_id: "acme" }));
        expect((await readJson(response)).plaintext).toBe("wyk_abc");
      },
    );
  });

  it("reads, renames and deletes one team", async () => {
    await runWithStub(
      [{ body: { tenant_id: "acme" } }, { body: { tenant_id: "acme", display_name: "Acme" } }, { body: { tenant_id: "acme", status: "deleted" } }],
      async (stub) => {
        await getTeam(makeRequest("http://localhost/api/admin/teams/acme"), teamParams("acme"));
        await patchTeam(
          makeRequest("http://localhost/api/admin/teams/acme", {
            method: "PATCH",
            body: JSON.stringify({ display_name: "Acme" }),
          }),
          teamParams("acme"),
        );
        await deleteTeam(
          makeRequest("http://localhost/api/admin/teams/acme", { method: "DELETE" }),
          teamParams("acme"),
        );
        expect(stub.calls).toEqual([
          "http://admin.internal:18890/admin/v1/teams/acme",
          "http://admin.internal:18890/admin/v1/teams/acme",
          "http://admin.internal:18890/admin/v1/teams/acme",
        ]);
        expect(stub.methods).toEqual(["GET", "PATCH", "DELETE"]);
        expect(stub.bodies[1]).toBe(JSON.stringify({ display_name: "Acme" }));
      },
    );
  });

  it("lists, adds and removes team members", async () => {
    await runWithStub([{ body: { members: [] } }, { body: { status: "ok" } }, { body: { status: "removed" } }], async (stub) => {
      await listTeamMembers(makeRequest("http://localhost/api/admin/teams/acme/members"), teamParams("acme"));
      await addTeamMember(
        makeRequest("http://localhost/api/admin/teams/acme/members/bob", {
          method: "PUT",
          body: JSON.stringify({ role: "owner" }),
        }),
        memberParams("acme", "bob"),
      );
      await removeTeamMember(
        makeRequest("http://localhost/api/admin/teams/acme/members/bob", { method: "DELETE" }),
        memberParams("acme", "bob"),
      );
      expect(stub.calls).toEqual([
        "http://admin.internal:18890/admin/v1/teams/acme/members",
        "http://admin.internal:18890/admin/v1/teams/acme/members/bob",
        "http://admin.internal:18890/admin/v1/teams/acme/members/bob",
      ]);
      expect(stub.methods).toEqual(["GET", "PUT", "DELETE"]);
      expect(stub.bodies[1]).toBe(JSON.stringify({ role: "owner" }));
    });
  });

  it("lists and creates team projects with the upstream 201", async () => {
    await runWithStub([{ body: { projects: [] } }, { status: 201, body: { project_id: "web" } }], async (stub) => {
      await listTeamProjects(makeRequest("http://localhost/api/admin/teams/acme/projects"), teamParams("acme"));
      const response = await createTeamProject(
        makeRequest("http://localhost/api/admin/teams/acme/projects", {
          method: "POST",
          body: JSON.stringify({ project_id: "web", name: "Web" }),
        }),
        teamParams("acme"),
      );
      expect(response.status).toBe(201);
      expect(stub.calls[1]).toBe("http://admin.internal:18890/admin/v1/teams/acme/projects");
      expect(stub.bodies[1]).toBe(JSON.stringify({ project_id: "web", name: "Web" }));
    });
  });

  it("deletes a project with the confirmation the upstream demands", async () => {
    await runWithStub(
      [{ body: { project_id: "web" } }, { body: { project_id: "web", deleted_banks: ["acme::Web"] } }],
      async (stub) => {
        await getProject(
          makeRequest("http://localhost/api/admin/teams/acme/projects/web"),
          projectParams("acme", "web"),
        );
        const response = await deleteProject(
          makeRequest("http://localhost/api/admin/teams/acme/projects/web", { method: "DELETE" }),
          projectParams("acme", "web"),
        );
        expect(stub.methods[1]).toBe("DELETE");
        // The body is built by the BFF, so the operator cannot delete a project
        // without confirming it and without accepting the bank deletion.
        expect(JSON.parse(stub.bodies[1] ?? "{}")).toEqual({ confirm: "web", delete_banks: true });
        expect(await readJson(response)).toEqual({ project_id: "web", deleted_banks: ["acme::Web"] });
      },
    );
  });

  it("manages project members and contribution purges", async () => {
    await runWithStub(
      [{ body: { members: [] } }, { body: { status: "ok" } }, { body: { status: "removed" } }, { body: { status: "purged", rebuild: false } }],
      async (stub) => {
        await listProjectMembers(
          makeRequest("http://localhost/api/admin/teams/acme/projects/web/members"),
          projectParams("acme", "web"),
        );
        await addProjectMember(
          makeRequest("http://localhost/api/admin/teams/acme/projects/web/members/bob", {
            method: "PUT",
            body: "{}",
          }),
          projectMemberParams("acme", "web", "bob"),
        );
        await removeProjectMember(
          makeRequest("http://localhost/api/admin/teams/acme/projects/web/members/bob", {
            method: "DELETE",
          }),
          projectMemberParams("acme", "web", "bob"),
        );
        const response = await purgeContribution(
          makeRequest("http://localhost/api/admin/teams/acme/projects/web/purge-contribution", {
            method: "POST",
            body: JSON.stringify({ tenant_id: "bob" }),
          }),
          projectParams("acme", "web"),
        );
        expect(stub.calls[3]).toBe(
          "http://admin.internal:18890/admin/v1/teams/acme/projects/web/purge-contribution",
        );
        expect(await readJson(response)).toEqual({ status: "purged", rebuild: false });
      },
    );
  });

  it("reports an upstream refusal with its status and without the internal error key", async () => {
    await runWithStub([{ status: 409, body: { error: "already present" } }], async () => {
      const response = await createTeamProject(
        makeRequest("http://localhost/api/admin/teams/acme/projects", {
          method: "POST",
          body: JSON.stringify({ project_id: "web", name: "Web" }),
        }),
        teamParams("acme"),
      );
      expect(response.status).toBe(409);
      const payload = await readJson(response);
      expect(JSON.stringify(payload)).not.toContain("errorKey");
    });
  });
});