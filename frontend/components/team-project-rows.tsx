"use client";

import { useTranslations } from "next-intl";
import type { ProjectMember, TeamMember, TeamProject } from "@/lib/admin-api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { TableCell, TableRow } from "@/components/ui/table";

/** ProjectRows renders one project, plus its membership when expanded. */
export function ProjectRows({
  project,
  expanded,
  members,
  candidates,
  candidate,
  onCandidate,
  onToggle,
  onAddMember,
  onRemoveMember,
  onDelete,
}: {
  project: TeamProject;
  expanded: boolean;
  members: ProjectMember[];
  candidates: TeamMember[];
  candidate: string;
  onCandidate: (value: string) => void;
  onToggle: () => void;
  onAddMember: (event: React.FormEvent) => void;
  onRemoveMember: (id: string) => void;
  onDelete: () => void;
}) {
  const t = useTranslations("teams.detail");
  const notOnProject = candidates.filter(
    (member) => !members.some((existing) => existing.tenant_id === member.tenant_id),
  );
  return (
    <>
      <TableRow>
        <TableCell className="font-mono text-[12px]">{project.project_id}</TableCell>
        <TableCell>{project.name}</TableCell>
        <TableCell className="font-mono text-[12px] text-muted-foreground">{project.bank_id}</TableCell>
        <TableCell className="text-right font-mono text-[12px]">{project.member_count}</TableCell>
        <TableCell className="text-right">
          <div className="flex justify-end gap-2">
            <Button size="sm" variant="outline" onClick={onToggle}>
              {expanded ? t("hideMembers") : t("showMembers")}
            </Button>
            <Button size="sm" variant="destructive" onClick={onDelete}>
              {t("delete")}
            </Button>
          </div>
        </TableCell>
      </TableRow>
      {expanded && (
        <TableRow>
          <TableCell colSpan={5} className="bg-muted/30">
            <form className="mb-3 flex flex-wrap items-center gap-2" onSubmit={onAddMember}>
              <Input
                value={candidate}
                onChange={(event) => onCandidate(event.target.value)}
                placeholder={t("projectMemberPlaceholder")}
                list={"project-member-candidates-" + project.project_id}
                className="max-w-xs"
              />
              <datalist id={"project-member-candidates-" + project.project_id}>
                {notOnProject.map((member) => (
                  <option key={member.tenant_id} value={member.tenant_id}>
                    {member.display_name}
                  </option>
                ))}
              </datalist>
              <Button size="sm" type="submit">
                {t("addProjectMember")}
              </Button>
            </form>
            <div className="flex flex-wrap gap-2">
              {members.length === 0 ? (
                <p className="text-[12px] text-muted-foreground">{t("noProjectMembers")}</p>
              ) : (
                members.map((member) => (
                  <span
                    key={member.tenant_id}
                    className="inline-flex items-center gap-2 rounded-full border border-border bg-card px-2.5 py-0.5 text-[12px]"
                  >
                    <span className="font-mono">{member.tenant_id}</span>
                    <button
                      type="button"
                      className="text-muted-foreground hover:text-destructive"
                      onClick={() => onRemoveMember(member.tenant_id)}
                    >
                      ×
                    </button>
                  </span>
                ))
              )}
            </div>
          </TableCell>
        </TableRow>
      )}
    </>
  );
}
