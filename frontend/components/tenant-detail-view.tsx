"use client";

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import { adminApi, type IssuedKey, type Tenant, type TenantKey } from "@/lib/admin-api";
import { ConsoleNav } from "@/components/console-nav";
import { PageHeader } from "@/components/ui/page-header";
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

function statusClass(status: string): string {
  if (status === "active") return "bg-emerald-500/10 text-emerald-600 dark:text-emerald-400";
  if (status === "suspended") return "bg-amber-500/10 text-amber-600 dark:text-amber-400";
  if (status === "deleted") return "bg-destructive/10 text-destructive";
  return "bg-muted text-muted-foreground";
}

export function TenantDetailView({ tenantId }: { tenantId: string }) {
  const router = useRouter();
  const t = useTranslations("tenants.detail");
  const ts = useTranslations("tenants.list.statuses");
  const tc = useTranslations("common");
  const [tenant, setTenant] = useState<Tenant | null>(null);
  const [keys, setKeys] = useState<TenantKey[]>([]);
  const [label, setLabel] = useState("");
  const [issued, setIssued] = useState<IssuedKey | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const [record, keyList] = await Promise.all([
        adminApi.getTenant(tenantId),
        adminApi.listKeys(tenantId),
      ]);
      setTenant(record);
      setKeys(keyList.keys ?? []);
    } catch (err) {
      setError(err instanceof Error ? err.message : t("loadFailed"));
    } finally {
      setLoading(false);
    }
  }, [tenantId]);

  useEffect(() => {
    void load();
  }, [load]);

  async function enterTenant() {
    try {
      await adminApi.enterTenant(tenantId);
      router.push("/dashboard");
      router.refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : t("enterFailed"));
    }
  }

  async function migrate() {
    setNotice(null);
    try {
      await adminApi.migrateTenant(tenantId);
      setNotice(t("migrated"));
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : t("migrateFailed"));
    }
  }

  async function purge() {
    if (!tenant || tenant.status !== "deleted") return;
    const confirm = window.prompt(t("purgeConfirm", { tenant: tenantId }));
    if (confirm !== tenantId) return;
    try {
      await adminApi.purgeTenant(tenantId, confirm);
      router.push("/tenants");
    } catch (err) {
      setError(err instanceof Error ? err.message : t("purgeFailed"));
    }
  }

  async function toggleStatus() {
    if (!tenant) return;
    const next = tenant.status === "active" ? "suspended" : "active";
    try {
      await adminApi.updateTenant(tenantId, { status: next });
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : t("statusFailed"));
    }
  }

  async function remove() {
    if (!tenant || !window.confirm(t("deleteConfirm", { tenant: tenantId }))) return;
    try {
      await adminApi.deleteTenant(tenantId);
      router.push("/tenants");
    } catch (err) {
      setError(err instanceof Error ? err.message : t("deleteFailed"));
    }
  }

  async function createKey(event: React.FormEvent) {
    event.preventDefault();
    try {
      const result = await adminApi.createKey(tenantId, { label: label || undefined });
      setIssued(result);
      setLabel("");
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : t("keyFailed"));
    }
  }

  async function revokeKey(keyId: string) {
    try {
      await adminApi.revokeKey(tenantId, keyId);
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : t("revokeFailed"));
    }
  }

  if (loading && !tenant) {
    return (
      <div className="min-h-screen bg-background">
        <ConsoleNav />
        <main className="mx-auto w-full max-w-5xl px-6 py-8 text-muted-foreground">
          {tc("loading")}
        </main>
      </div>
    );
  }

  if (!tenant) {
    return (
      <div className="min-h-screen bg-background">
        <ConsoleNav />
        <main className="mx-auto w-full max-w-5xl px-6 py-8">
          <p className="text-destructive">{error ?? t("notFound")}</p>
          <Button variant="outline" className="mt-4" asChild>
            <Link href="/tenants">{tc("back")}</Link>
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
          eyebrow="tenant"
          title={tenant.tenant_id}
          description={tenant.display_name || "-"}
          actions={
            <div className="flex gap-2">
              <Button onClick={() => void enterTenant()}>{t("enter")}</Button>
              {tenant.status !== "deleted" && (
                <Button variant="outline" onClick={() => void migrate()}>
                  {t("migrate")}
                </Button>
              )}
              {tenant.status === "deleted" && (
                <Button variant="destructive" onClick={() => void purge()}>
                  {t("purge")}
                </Button>
              )}
              <Button variant="outline" onClick={() => void toggleStatus()}>
                {tenant.status === "active" ? t("suspend") : t("activate")}
              </Button>
              <Button variant="destructive" onClick={() => void remove()}>
                {tc("delete")}
              </Button>
              <Button variant="outline" asChild>
                <Link href="/tenants">{tc("back")}</Link>
              </Button>
            </div>
          }
        />
        {error && (
          <Card className="mb-4 border-destructive/40">
            <CardContent className="py-4 text-sm text-destructive">{error}</CardContent>
          </Card>
        )}
        {notice && (
          <Card className="mb-4 border-emerald-500/40">
            <CardContent className="py-4 text-sm text-emerald-600 dark:text-emerald-400">
              {notice}
            </CardContent>
          </Card>
        )}

        <div className="mb-6 flex items-center gap-3 text-sm">
          <span className={"rounded-full px-2 py-0.5 text-[11px] font-medium " + statusClass(tenant.status)}>
            {ts(
              tenant.status === "active"
                ? "active"
                : tenant.status === "suspended"
                  ? "suspended"
                  : tenant.status === "provisioning"
                    ? "provisioning"
                    : "deleted",
            )}
          </span>
          <span className="text-muted-foreground">
            {t("schema")} <span className="font-mono">{tenant.tenant_id}</span>
          </span>
        </div>

        <Card className="mb-6">
          <CardHeader>
            <CardTitle className="text-[15px]">{t("config")}</CardTitle>
          </CardHeader>
          <CardContent>
            <pre className="overflow-x-auto rounded-[8px] bg-muted/50 p-3 font-mono text-[12px]">
              {JSON.stringify(tenant.config ?? {}, null, 2)}
            </pre>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="text-[15px]">{t("keys")}</CardTitle>
          </CardHeader>
          <CardContent className="space-y-4">
            <form className="flex flex-wrap items-center gap-2" onSubmit={createKey}>
              <Input
                value={label}
                onChange={(event) => setLabel(event.target.value)}
                placeholder={t("keyLabel")}
                className="max-w-xs"
              />
              <Button type="submit">{t("createKey")}</Button>
            </form>
            {issued && (
              <div className="rounded-[9px] border border-primary/30 bg-primary/[0.06] p-3">
                <p className="text-[12px] font-medium">{t("shownOnce")}</p>
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
                  <TableHead>{t("prefix")}</TableHead>
                  <TableHead>{t("label")}</TableHead>
                  <TableHead>{t("created")}</TableHead>
                  <TableHead>{t("state")}</TableHead>
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
                      <TableCell>
                        {key.revoked_at ? t("revoked") : key.ephemeral ? t("ephemeral") : t("active")}
                      </TableCell>
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
      </main>
    </div>
  );
}
