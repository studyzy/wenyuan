"use client";

import { useTranslations } from "next-intl";
import type { TeamMember, Tenant, TeamRole } from "@/lib/admin-api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";

/**
 * TeamMembersPanel is the team's membership list and its add form.
 *
 * It is its own component so the team page stays readable: the panel owns no
 * state, it reports a membership change and the page reloads.
 */
export function TeamMembersPanel({
  members,
  candidates,
  memberId,
  role,
  onMemberId,
  onRole,
  onAdd,
  onRemove,
}: {
  members: TeamMember[];
  candidates: Tenant[];
  memberId: string;
  role: TeamRole;
  onMemberId: (value: string) => void;
  onRole: (role: TeamRole) => void;
  onAdd: (event: React.FormEvent) => void;
  onRemove: (tenantId: string) => void;
}) {
  const t = useTranslations("teams.detail");
  return (
  <Card>
    <CardHeader>
      <CardTitle className="text-[15px]">{t("members")}</CardTitle>
    </CardHeader>
    <CardContent className="space-y-4">
      <form className="flex flex-wrap items-center gap-2" onSubmit={onAdd}>
        <Input
          value={memberId}
          onChange={(event) => onMemberId(event.target.value)}
          placeholder={t("memberIdPlaceholder")}
          list="team-member-candidates"
          className="max-w-xs"
        />
        <datalist id="team-member-candidates">
          {candidates.map((candidate) => (
            <option key={candidate.tenant_id} value={candidate.tenant_id}>
              {candidate.display_name}
            </option>
          ))}
        </datalist>
        <select
          value={role}
          onChange={(event) => onRole(event.target.value as TeamRole)}
          className="h-9 rounded-[9px] border border-input bg-card px-3 text-[13px]"
        >
          <option value="member">{t("roles.member")}</option>
          <option value="owner">{t("roles.owner")}</option>
        </select>
        <Button type="submit">{t("addMember")}</Button>
      </form>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{t("tenant")}</TableHead>
            <TableHead>{t("displayName")}</TableHead>
            <TableHead>{t("role")}</TableHead>
            <TableHead>{t("joined")}</TableHead>
            <TableHead />
          </TableRow>
        </TableHeader>
        <TableBody>
          {members.length === 0 ? (
            <TableRow>
              <TableCell colSpan={5} className="py-6 text-center text-muted-foreground">
                {t("noMembers")}
              </TableCell>
            </TableRow>
          ) : (
            members.map((member) => (
              <TableRow key={member.tenant_id}>
                <TableCell className="font-mono text-[12px]">{member.tenant_id}</TableCell>
                <TableCell>{member.display_name || "-"}</TableCell>
                <TableCell>{member.role === "owner" ? t("roles.owner") : t("roles.member")}</TableCell>
                <TableCell className="text-muted-foreground">
                  {member.joined_at ? new Date(member.joined_at).toLocaleString() : "-"}
                </TableCell>
                <TableCell className="text-right">
                  <Button size="sm" variant="outline" onClick={() => onRemove(member.tenant_id)}>
                    {t("remove")}
                  </Button>
                </TableCell>
              </TableRow>
            ))
          )}
        </TableBody>
      </Table>
    </CardContent>
  </Card>
  );
}
