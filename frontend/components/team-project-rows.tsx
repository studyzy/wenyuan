"use client";

import { useTranslations } from "next-intl";
import type { ProjectMember, TeamMember, TeamProject } from "@/lib/admin-api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { TableCell, TableRow } from "@/components/ui/table";

/**
 * ProjectRows renders one project, plus its membership when expanded.
 *
 * A member has two distinct exits, and the panel names both rather than leaving a
 * bare glyph to be discovered:
 *
 *   - remove takes them off the project. Their ledger rows go with them, so their
 *     new facts stop arriving, but what was already merged stays in the team's
 *     shared content.
 *   - withdraw removes them *and* rebuilds, so what they contributed leaves the
 *     team's content too.
 *
 * The distinction matters enough to be spelled out: "I added the wrong person" and
 * "this person should no longer be quoted" have different answers, and getting the
 * gentler one when the stronger was meant leaves their facts in place.
 */
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
  onPurgeMember,
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
  onPurgeMember: (id: string) => void;
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
            {members.length === 0 ? (
              <p className="text-[12px] text-muted-foreground">{t("noProjectMembers")}</p>
            ) : (
              <div className="space-y-1">
                {members.map((member) => (
                  <div
                    key={member.tenant_id}
                    className="flex flex-wrap items-center justify-between gap-2 rounded-[10px] border border-border bg-card px-3 py-1.5"
                  >
                    <span className="text-[12px]">
                      <span className="font-mono">{member.tenant_id}</span>
                      {member.display_name && member.display_name !== member.tenant_id ? (
                        <span className="ml-2 text-muted-foreground">{member.display_name}</span>
                      ) : null}
                    </span>
                    <span className="flex gap-2">
                      <Button
                        size="sm"
                        variant="outline"
                        title={t("removeProjectMemberHint", { member: member.tenant_id })}
                        onClick={() => onRemoveMember(member.tenant_id)}
                      >
                        {t("removeProjectMember")}
                      </Button>
                      <Button
                        size="sm"
                        variant="destructive"
                        title={t("purgeProjectMemberHint", { member: member.tenant_id })}
                        onClick={() => onPurgeMember(member.tenant_id)}
                      >
                        {t("purgeProjectMember")}
                      </Button>
                    </span>
                  </div>
                ))}
              </div>
            )}
          </TableCell>
        </TableRow>
      )}
    </>
  );
}