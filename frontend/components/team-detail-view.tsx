"use client";

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import {
  adminApi,
  type IssuedKey,
  type ProjectMember as ProjectMemberRecord,
  type Team,
  type TenantKey,
  type TeamMember,
  type TeamProject,
  type Tenant,
  type TeamRole,
} from "@/lib/admin-api";
import { ConsoleNav } from "@/components/console-nav";
import { PageHeader } from "@/components/ui/page-header";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { ProjectRows } from "@/components/team-project-rows";
import { TeamMembersPanel } from "@/components/team-members-panel";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";

type Notice = { kind: "error" | "ok"; text: string } | null;

export function TeamDetailView({ teamId }: { teamId: string }) {
  const router = useRouter();
  const t = useTranslations("teams.detail");
  const tc = useTranslations("common");
  const [team, setTeam] = useState<Team | null>(null);
  const [members, setMembers] = useState<TeamMember[]>([]);
  const [projects, setProjects] = useState<TeamProject[]>([]);
  const [tenants, setTenants] = useState<Tenant[]>([]);
  const [keys, setKeys] = useState<TenantKey[]>([]);
  const [issued, setIssued] = useState<IssuedKey | null>(null);
  const [keyLabel, setKeyLabel] = useState("");
  const [projectMembers, setProjectMembers] = useState<Record<string, ProjectMemberRecord[]>>({});
  const [expanded, setExpanded] = useState<string | null>(null);
  const [memberId, setMemberId] = useState("");
  const [role, setRole] = useState<TeamRole>("member");
  const [projectId, setProjectId] = useState("");
  const [projectName, setProjectName] = useState("");
  const [projectMemberId, setProjectMemberId] = useState<Record<string, string>>({});
  const [notice, setNotice] = useState<Notice>(null);
  const [loading, setLoading] = useState(true);

  const fail = useCallback((err: unknown, key: string) => {
    setNotice({ kind: "error", text: err instanceof Error ? err.message : t(key) });
  }, [t]);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const [record, memberList, projectList, keyList] = await Promise.all([
        adminApi.getTeam(teamId),
        adminApi.listTeamMembers(teamId),
        adminApi.listTeamProjects(teamId),
        adminApi.listTeamKeys(teamId),
      ]);
      setTeam(record);
      setMembers(memberList.members ?? []);
      setProjects(projectList.projects ?? []);
      setKeys(keyList.keys ?? []);
    } catch (err) {
      fail(err, "loadFailed");
    } finally {
      setLoading(false);
    }
    // Candidates for membership are the deployment's personal tenants.
    try {
      const candidates = await adminApi.listTenants({ status: "active", kind: "personal" });
      setTenants(candidates.tenants ?? []);
    } catch {
      // The picker is an aid; without it the id field still works.
      setTenants([]);
    }
  }, [teamId, fail]);

  useEffect(() => {
    void load();
  }, [load]);

  async function addMember(event: React.FormEvent) {
    event.preventDefault();
    if (!memberId.trim()) return;
    setNotice(null);
    try {
      await adminApi.addTeamMember(teamId, memberId.trim(), role);
      setMemberId("");
      setNotice({ kind: "ok", text: t("memberAdded") });
      await load();
    } catch (err) {
      fail(err, "memberFailed");
    }
  }

  async function removeMember(id: string) {
    if (!window.confirm(t("memberRemoveConfirm", { tenant: id }))) return;
    setNotice(null);
    try {
      await adminApi.removeTeamMember(teamId, id);
      await load();
    } catch (err) {
      fail(err, "memberFailed");
    }
  }

  async function createProject(event: React.FormEvent) {
    event.preventDefault();
    setNotice(null);
    if (!/^[a-z][a-z0-9_]{1,62}$/.test(projectId)) {
      setNotice({ kind: "error", text: t("projectIdInvalid") });
      return;
    }
    if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/.test(projectName)) {
      setNotice({ kind: "error", text: t("projectNameInvalid") });
      return;
    }
    try {
      await adminApi.createTeamProject(teamId, { project_id: projectId, name: projectName });
      setProjectId("");
      setProjectName("");
      setNotice({ kind: "ok", text: t("projectCreated") });
      await load();
    } catch (err) {
      fail(err, "projectFailed");
    }
  }

  async function deleteProject(project: TeamProject) {
    // Deleting a project deletes its banks, so the confirmation names the
    // project and the operation is one call the moment it is typed.
    const typed = window.prompt(t("projectDeleteConfirm", { project: project.project_id, bank: project.bank_id }));
    if (typed !== project.project_id) return;
    setNotice(null);
    try {
      const result = await adminApi.deleteTeamProject(teamId, project.project_id);
      setNotice({ kind: "ok", text: t("projectDeleted", { count: (result.deleted_banks ?? []).length }) });
      await load();
    } catch (err) {
      fail(err, "projectFailed");
    }
  }

  async function toggleProject(project: TeamProject) {
    if (expanded === project.project_id) {
      setExpanded(null);
      return;
    }
    setExpanded(project.project_id);
    try {
      const list = await adminApi.listProjectMembers(teamId, project.project_id);
      setProjectMembers((current) => ({ ...current, [project.project_id]: list.members ?? [] }));
    } catch (err) {
      fail(err, "projectFailed");
    }
  }

  async function addProjectMember(project: TeamProject, event: React.FormEvent) {
    event.preventDefault();
    const candidate = (projectMemberId[project.project_id] ?? "").trim();
    if (!candidate) return;
    setNotice(null);
    try {
      await adminApi.addProjectMember(teamId, project.project_id, candidate);
      const list = await adminApi.listProjectMembers(teamId, project.project_id);
      setProjectMembers((current) => ({ ...current, [project.project_id]: list.members ?? [] }));
      setProjectMemberId((current) => ({ ...current, [project.project_id]: "" }));
      await load();
    } catch (err) {
      fail(err, "projectFailed");
    }
  }

  async function removeProjectMember(project: TeamProject, id: string) {
    setNotice(null);
    try {
      await adminApi.removeProjectMember(teamId, project.project_id, id);
      const list = await adminApi.listProjectMembers(teamId, project.project_id);
      setProjectMembers((current) => ({ ...current, [project.project_id]: list.members ?? [] }));
      await load();
    } catch (err) {
      fail(err, "projectFailed");
    }
  }

  async function createKey(event: React.FormEvent) {
    event.preventDefault();
    setNotice(null);
    try {
      setIssued(await adminApi.createTeamKey(teamId, { label: keyLabel || undefined }));
      setKeyLabel("");
      await load();
    } catch (err) {
      fail(err, "keyFailed");
    }
  }

  async function revokeKey(keyId: string) {
    setNotice(null);
    try {
      await adminApi.revokeTeamKey(teamId, keyId);
      await load();
    } catch (err) {
      fail(err, "keyFailed");
    }
  }

  async function rename() {
    if (!team) return;
    const name = window.prompt(t("renamePrompt"), team.display_name);
    if (!name || name.trim() === "" || name === team.display_name) return;
    setNotice(null);
    try {
      setTeam(await adminApi.renameTeam(teamId, name.trim()));
    } catch (err) {
      fail(err, "renameFailed");
    }
  }

  async function remove() {
    if (!window.confirm(t("deleteConfirm", { team: teamId }))) return;
    try {
      await adminApi.deleteTeam(teamId);
      router.push("/teams");
    } catch (err) {
      fail(err, "deleteFailed");
    }
  }

  if (loading && !team) {
    return (
      <div className="min-h-screen bg-background">
        <ConsoleNav />
        <main className="mx-auto w-full max-w-5xl px-6 py-8 text-muted-foreground">{tc("loading")}</main>
      </div>
    );
  }

  if (!team) {
    return (
      <div className="min-h-screen bg-background">
        <ConsoleNav />
        <main className="mx-auto w-full max-w-5xl px-6 py-8">
          <p className="text-destructive">{notice?.text ?? t("notFound")}</p>
          <Button variant="outline" className="mt-4" asChild>
            <Link href="/teams">{tc("back")}</Link>
          </Button>
        </main>
      </div>
    );
  }

  return (
    <div className="min-h-screen bg-background">
      <ConsoleNav />
      <main className="mx-auto w-full max-w-5xl px-6 py-8">
        <PageHeader
          eyebrow="team"
          title={team.tenant_id}
          description={team.display_name || "-"}
          actions={
            <div className="flex gap-2">
              <Button variant="outline" onClick={() => void rename()}>
                {t("rename")}
              </Button>
              <Button variant="destructive" onClick={() => void remove()}>
                {t("delete")}
              </Button>
              <Button variant="outline" asChild>
                <Link href="/teams">{tc("back")}</Link>
              </Button>
            </div>
          }
        />
        {notice && (
          <Card className={"mb-4 " + (notice.kind === "error" ? "border-destructive/40" : "border-emerald-500/40")}>
            <CardContent
              className={
                "py-4 text-sm " + (notice.kind === "error" ? "text-destructive" : "text-emerald-600 dark:text-emerald-400")
              }
            >
              {notice.text}
            </CardContent>
          </Card>
        )}

        <Tabs defaultValue="members" className="space-y-4">
          <TabsList>
            <TabsTrigger value="members">{t("tabs.members")}</TabsTrigger>
            <TabsTrigger value="projects">{t("tabs.projects")}</TabsTrigger>
            <TabsTrigger value="keys">{t("tabs.keys")}</TabsTrigger>
          </TabsList>

          <TabsContent value="members">
            <TeamMembersPanel
              members={members}
              candidates={tenants}
              memberId={memberId}
              role={role}
              onMemberId={setMemberId}
              onRole={setRole}
              onAdd={(event) => void addMember(event)}
              onRemove={(id) => void removeMember(id)}
            />
          </TabsContent>

          <TabsContent value="projects">
            <Card>
              <CardHeader>
                <CardTitle className="text-[15px]">{t("projects")}</CardTitle>
              </CardHeader>
              <CardContent className="space-y-4">
                <form className="flex flex-wrap items-center gap-2" onSubmit={createProject}>
                  <Input
                    value={projectId}
                    onChange={(event) => setProjectId(event.target.value)}
                    placeholder={t("projectIdPlaceholder")}
                    className="max-w-[180px]"
                  />
                  <Input
                    value={projectName}
                    onChange={(event) => setProjectName(event.target.value)}
                    placeholder={t("projectNamePlaceholder")}
                    className="max-w-[180px]"
                  />
                  <Button type="submit">{t("createProject")}</Button>
                  <span className="text-[12px] text-muted-foreground">{t("projectHint")}</span>
                </form>
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>{t("project")}</TableHead>
                      <TableHead>{t("projectName")}</TableHead>
                      <TableHead>{t("bank")}</TableHead>
                      <TableHead className="text-right">{t("projectMembers")}</TableHead>
                      <TableHead />
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {projects.length === 0 ? (
                      <TableRow>
                        <TableCell colSpan={5} className="py-6 text-center text-muted-foreground">
                          {t("noProjects")}
                        </TableCell>
                      </TableRow>
                    ) : (
                      projects.map((project) => (
                        <ProjectRows
                          key={project.project_id}
                          project={project}
                          expanded={expanded === project.project_id}
                          members={projectMembers[project.project_id] ?? []}
                          candidates={members}
                          candidate={projectMemberId[project.project_id] ?? ""}
                          onCandidate={(value) =>
                            setProjectMemberId((current) => ({ ...current, [project.project_id]: value }))
                          }
                          onToggle={() => void toggleProject(project)}
                          onAddMember={(event) => void addProjectMember(project, event)}
                          onRemoveMember={(id) => void removeProjectMember(project, id)}
                          onDelete={() => void deleteProject(project)}
                        />
                      ))
                    )}
                  </TableBody>
                </Table>
              </CardContent>
            </Card>
          </TabsContent>
          <TabsContent value="keys">
            <Card>
              <CardHeader>
                <CardTitle className="text-[15px]">{t("keys")}</CardTitle>
              </CardHeader>
              <CardContent className="space-y-4">
                <p className="text-[12px] text-muted-foreground">{t("keysHint")}</p>
                <form className="flex flex-wrap items-center gap-2" onSubmit={createKey}>
                  <Input
                    value={keyLabel}
                    onChange={(event) => setKeyLabel(event.target.value)}
                    placeholder={t("keyLabel")}
                    className="max-w-xs"
                  />
                  <Button type="submit">{t("createKey")}</Button>
                </form>
                {issued && (
                  <div className="rounded-[9px] border border-primary/30 bg-primary/[0.06] p-3">
                    <p className="text-[12px] font-medium">{t("keyShownOnce")}</p>
                    <div className="mt-2 flex items-center gap-2">
                      <code className="min-w-0 flex-1 truncate font-mono text-[12px]">{issued.plaintext}</code>
                      <Button
                        size="sm"
                        variant="outline"
                        onClick={() => void navigator.clipboard?.writeText(issued.plaintext)}
                      >
                        {tc("copy")}
                      </Button>
                    </div>
                  </div>
                )}
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>{t("keyPrefix")}</TableHead>
                      <TableHead>{t("keyLabelColumn")}</TableHead>
                      <TableHead>{t("createdAt")}</TableHead>
                      <TableHead>{t("keyState")}</TableHead>
                      <TableHead />
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {keys.length === 0 ? (
                      <TableRow>
                        <TableCell colSpan={5} className="py-6 text-center text-muted-foreground">
                          {t("noKeys")}
                        </TableCell>
                      </TableRow>
                    ) : (
                      keys.map((key) => (
                        <TableRow key={key.key_id}>
                          <TableCell className="font-mono text-[12px]">{key.prefix}</TableCell>
                          <TableCell>{key.label}</TableCell>
                          <TableCell className="text-muted-foreground">
                            {key.created_at ? new Date(key.created_at).toLocaleString() : "-"}
                          </TableCell>
                          <TableCell>{key.revoked_at ? t("keyRevoked") : t("keyActive")}</TableCell>
                          <TableCell className="text-right">
                            {!key.revoked_at && (
                              <Button size="sm" variant="outline" onClick={() => void revokeKey(key.key_id)}>
                                {t("revoke")}
                              </Button>
                            )}
                          </TableCell>
                        </TableRow>
                      ))
                    )}
                  </TableBody>
                </Table>
              </CardContent>
            </Card>
          </TabsContent>
        </Tabs>
      </main>
    </div>
  );
}
