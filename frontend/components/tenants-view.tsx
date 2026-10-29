"use client";

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { useTranslations } from "next-intl";
import { adminApi, type Tenant } from "@/lib/admin-api";
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
  active: "list.statuses.active",
  suspended: "list.statuses.suspended",
  provisioning: "list.statuses.provisioning",
  deleted: "list.statuses.deleted",
};

function statusClass(status: string): string {
  if (status === "active") return "bg-emerald-500/10 text-emerald-600 dark:text-emerald-400";
  if (status === "suspended") return "bg-amber-500/10 text-amber-600 dark:text-amber-400";
  if (status === "deleted") return "bg-destructive/10 text-destructive";
  return "bg-muted text-muted-foreground";
}

export function TenantsView() {
  const t = useTranslations("tenants");
  const tc = useTranslations("common");
  const [tenants, setTenants] = useState<Tenant[]>([]);
  const [query, setQuery] = useState("");
  const [status, setStatus] = useState("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const response = await adminApi.listTenants({ q: query, status });
      setTenants(response.tenants ?? []);
    } catch (err) {
      setError(err instanceof Error ? err.message : t("list.failed"));
    } finally {
      setLoading(false);
    }
  }, [query, status]);

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
              <Link href="/tenants/new">{t("list.new")}</Link>
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
          <select
            value={status}
            onChange={(event) => setStatus(event.target.value)}
            className="h-9 rounded-[9px] border border-input bg-card px-3 text-[13px]"
          >
            <option value="">{t("list.allStatuses")}</option>
            <option value="active">{t("list.statuses.active")}</option>
            <option value="suspended">{t("list.statuses.suspended")}</option>
            <option value="provisioning">{t("list.statuses.provisioning")}</option>
            <option value="deleted">{t("list.statuses.deleted")}</option>
          </select>
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
                  <TableHead>{t("list.tenant")}</TableHead>
                  <TableHead>{t("list.status")}</TableHead>
                  <TableHead>{t("list.displayName")}</TableHead>
                  <TableHead>{t("list.created")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {loading ? (
                  <TableRow>
                    <TableCell colSpan={4} className="py-8 text-center text-muted-foreground">
                      {tc("loading")}
                    </TableCell>
                  </TableRow>
                ) : tenants.length === 0 ? (
                  <TableRow>
                    <TableCell colSpan={4} className="py-8 text-center text-muted-foreground">
                      {t("list.empty")}
                    </TableCell>
                  </TableRow>
                ) : (
                  tenants.map((tenant) => (
                    <TableRow key={tenant.tenant_id}>
                      <TableCell className="font-mono text-[12px]">
                        <Link className="text-primary hover:underline" href={"/tenants/" + tenant.tenant_id}>
                          {tenant.tenant_id}
                        </Link>
                      </TableCell>
                      <TableCell>
                        <span className={"rounded-full px-2 py-0.5 text-[11px] font-medium " + statusClass(tenant.status)}>
                          {t(STATUS_KEYS[tenant.status] ?? "list.statuses.provisioning")}
                        </span>
                      </TableCell>
                      <TableCell>{tenant.display_name}</TableCell>
                      <TableCell className="text-muted-foreground">
                        {tenant.created_at ? new Date(tenant.created_at).toLocaleString() : "-"}
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
