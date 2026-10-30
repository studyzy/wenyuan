"use client";

import { useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import { adminApi, type CreatedTeam } from "@/lib/admin-api";
import { ConsoleNav } from "@/components/console-nav";
import { PageHeader } from "@/components/ui/page-header";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent } from "@/components/ui/card";

export function TeamCreateForm() {
  const router = useRouter();
  const t = useTranslations("teams.create");
  const tc = useTranslations("common");
  const [tenantId, setTeamId] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [created, setCreated] = useState<CreatedTeam | null>(null);

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    setError(null);
    // A team is a tenant, so its id becomes the schema name and obeys the same
    // shape the tenant form enforces.
    if (!/^[a-z][a-z0-9_]{1,62}$/.test(tenantId)) {
      setError(t("invalidId"));
      return;
    }
    setSaving(true);
    try {
      const result = await adminApi.createTeam({
        tenant_id: tenantId,
        display_name: displayName || undefined,
      });
      // The key is shown once, so the form stays here to show it rather than
      // navigating away from it.
      setCreated(result);
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
              <Link href="/teams">{tc("back")}</Link>
            </Button>
          }
        />
        {created ? (
          <Card>
            <CardContent className="space-y-4 pt-6">
              <p className="text-[15px] font-medium">{t("succeeded")}</p>
              <div className="rounded-[9px] border border-primary/30 bg-primary/[0.06] p-3">
                <p className="text-[12px] font-medium">{t("shownOnce")}</p>
                <div className="mt-2 flex items-center gap-2">
                  <code className="min-w-0 flex-1 truncate font-mono text-[12px]">{created.plaintext}</code>
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => void navigator.clipboard?.writeText(created.plaintext)}
                  >
                    {tc("copy")}
                  </Button>
                </div>
              </div>
              <div className="flex gap-2">
                <Button asChild>
                  <Link href={"/teams/" + encodeURIComponent(created.team.tenant_id)}>{t("openTeam")}</Link>
                </Button>
                <Button
                  variant="outline"
                  onClick={() => {
                    setCreated(null);
                    setTeamId("");
                    setDisplayName("");
                    router.refresh();
                  }}
                >
                  {t("createAnother")}
                </Button>
              </div>
            </CardContent>
          </Card>
        ) : (
          <Card>
            <CardContent className="pt-6">
              <form className="space-y-4" onSubmit={submit}>
                <label className="block space-y-1.5">
                  <span className="text-[13px] font-medium">{t("id")}</span>
                  <Input
                    value={tenantId}
                    onChange={(event) => setTeamId(event.target.value)}
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
                {error && <p className="text-sm text-destructive">{error}</p>}
                <div className="flex gap-2">
                  <Button type="submit" disabled={saving}>
                    {saving ? t("creating") : t("submit")}
                  </Button>
                  <Button type="button" variant="outline" asChild>
                    <Link href="/teams">{tc("cancel")}</Link>
                  </Button>
                </div>
              </form>
            </CardContent>
          </Card>
        )}
      </main>
    </div>
  );
}