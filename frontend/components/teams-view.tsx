"use client";

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { useTranslations } from "next-intl";
import { adminApi, type Team } from "@/lib/admin-api";
import { ConsoleNav } from "@/components/console-nav";
import { PageHeader } from "@/components/ui/page-header";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";

const STATUS_KEYS: Record<string, string> = {
  active: "statuses.active",
  suspended: "statuses.suspended",
  provisioning: "statuses.provisioning",
  deleted: "statuses.deleted",
};

function statusClass(status: string): string {
  if (status === "active") return "bg-emerald-500/10 text-emerald-600 dark:text-emerald-400";
  if (status === "suspended") return "bg-amber-500/10 text-amber-600 dark:text-amber-400";
  if (status === "deleted") return "bg-destructive/10 text-destructive";
  return "bg-muted text-muted-foreground";
}

export function TeamsView() {
  const t = useTranslations("teams");
  const tc = useTranslations("common");
  const [teams, setTeams] = useState<Team[]>([]);
  const [query, setQuery] = useState("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const response = await adminApi.listTeams({ q: query });
      setTeams(response.teams ?? []);
    } catch (err) {
      setError(err instanceof Error ? err.message : t("list.failed"));
    } finally {
      setLoading(false);
    }
  }, [query, t]);

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <div className="min-h-screen bg-background">
      <ConsoleNav />
      <main className="mx-auto w-full max-w-6xl px-6 py-8">
        <PageHeader
          eyebrow="management"
          title={t("list.title")}
          description={t("list.description")}
          actions={
            <Button asChild>
              <Link href="/teams/new">{t("list.new")}</Link>
            </Button>
          }
        />

        <div className="mb-4 flex flex-wrap items-center gap-2">
          <Input
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder={t("list.searchPlaceholder")}
            className="max-w-xs"
          />
          <Button variant="outline" onClick={() => void load()}>
            {tc("refresh")}
          </Button>
        </div>

        {error && (
          <Card className="mb-4 border-destructive/40">
            <CardContent className="py-4 text-sm text-destructive">{error}</CardContent>
          </Card>
        )}

        <Card>
          <CardContent className="p-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("list.team")}</TableHead>
                  <TableHead>{t("list.status")}</TableHead>
                  <TableHead>{t("list.displayName")}</TableHead>
                  <TableHead className="text-right">{t("list.members")}</TableHead>
                  <TableHead className="text-right">{t("list.projects")}</TableHead>
                  <TableHead>{t("list.created")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {loading ? (
                  <TableRow>
                    <TableCell colSpan={6} className="py-8 text-center text-muted-foreground">
                      {tc("loading")}
                    </TableCell>
                  </TableRow>
                ) : teams.length === 0 ? (
                  <TableRow>
                    <TableCell colSpan={6} className="py-8 text-center text-muted-foreground">
                      {t("list.empty")}
                    </TableCell>
                  </TableRow>
                ) : (
                  teams.map((team) => (
                    <TableRow key={team.tenant_id}>
                      <TableCell className="font-mono text-[12px]">
                        <Link className="text-primary hover:underline" href={"/teams/" + encodeURIComponent(team.tenant_id)}>
                          {team.tenant_id}
                        </Link>
                      </TableCell>
                      <TableCell>
                        <span className={"rounded-full px-2 py-0.5 text-[11px] font-medium " + statusClass(team.status)}>
                          {t(STATUS_KEYS[team.status] ?? "statuses.provisioning")}
                        </span>
                      </TableCell>
                      <TableCell>{team.display_name}</TableCell>
                      <TableCell className="text-right font-mono text-[12px]">{team.member_count}</TableCell>
                      <TableCell className="text-right font-mono text-[12px]">{team.project_count}</TableCell>
                      <TableCell className="text-muted-foreground">
                        {team.created_at ? new Date(team.created_at).toLocaleString() : "-"}
                      </TableCell>
                    </TableRow>
                  ))
                )}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      </main>
    </div>
  );
}