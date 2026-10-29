"use client";

import { useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import { adminApi } from "@/lib/admin-api";
import { ConsoleNav } from "@/components/console-nav";
import { PageHeader } from "@/components/ui/page-header";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent } from "@/components/ui/card";

export function TenantCreateForm() {
  const router = useRouter();
  const t = useTranslations("tenants.create");
  const tc = useTranslations("common");
  const [tenantId, setTenantId] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [configText, setConfigText] = useState("");
  const [adopt, setAdopt] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    setError(null);
    if (!/^[a-z][a-z0-9_]{1,62}$/.test(tenantId)) {
      setError(t("invalidId"));
      return;
    }
    let config: Record<string, unknown> | undefined;
    if (configText.trim()) {
      try {
        config = JSON.parse(configText) as Record<string, unknown>;
      } catch {
        setError(t("invalidConfig"));
        return;
      }
    }
    setSaving(true);
    try {
      await adminApi.createTenant({
        tenant_id: tenantId,
        display_name: displayName || undefined,
        config,
        adopt,
      });
      router.push("/tenants/" + encodeURIComponent(tenantId));
    } catch (err) {
      setError(err instanceof Error ? err.message : t("failed"));
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="min-h-screen bg-background">
      <ConsoleNav />
      <main className="mx-auto w-full max-w-2xl px-6 py-8">
        <PageHeader
          eyebrow="management"
          title={t("title")}
          description={t("description")}
          actions={
            <Button variant="outline" asChild>
              <Link href="/tenants">{tc("back")}</Link>
            </Button>
          }
        />
        <Card>
          <CardContent className="pt-6">
            <form className="space-y-4" onSubmit={submit}>
              <label className="block space-y-1.5">
                <span className="text-[13px] font-medium">{t("id")}</span>
                <Input
                  value={tenantId}
                  onChange={(event) => setTenantId(event.target.value)}
                  placeholder={t("idPlaceholder")}
                  autoFocus
                />
                <span className="text-[12px] text-muted-foreground">{t("idHint")}</span>
              </label>
              <label className="block space-y-1.5">
                <span className="text-[13px] font-medium">{t("displayName")}</span>
                <Input
                  value={displayName}
                  onChange={(event) => setDisplayName(event.target.value)}
                  placeholder={t("displayNamePlaceholder")}
                />
              </label>
              <label className="block space-y-1.5">
                <span className="text-[13px] font-medium">{t("config")}</span>
                <textarea
                  value={configText}
                  onChange={(event) => setConfigText(event.target.value)}
                  placeholder='{"enable_text_search": false}'
                  className="min-h-24 w-full rounded-[9px] border border-input bg-card px-3 py-2 font-mono text-[12px]"
                />
              </label>
              <label className="flex items-center gap-2 text-[13px]">
                <input
                  type="checkbox"
                  checked={adopt}
                  onChange={(event) => setAdopt(event.target.checked)}
                />
                {t("adopt")}
              </label>
              {error && <p className="text-sm text-destructive">{error}</p>}
              <div className="flex gap-2">
                <Button type="submit" disabled={saving}>
                  {saving ? t("creating") : t("submit")}
                </Button>
                <Button type="button" variant="outline" asChild>
                  <Link href="/tenants">{tc("cancel")}</Link>
                </Button>
              </div>
            </form>
          </CardContent>
        </Card>
      </main>
    </div>
  );
}
