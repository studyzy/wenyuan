"use client";

import { Suspense, useState, FormEvent, useEffect } from "react";
import { useSearchParams, useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import { AlertCircle, ArrowRight } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Eyebrow } from "@/components/ui/page-header";
import { LanguageSwitcher } from "@/components/language-switcher";
import { sanitizeReturnTo, withBasePath } from "@/lib/base-path";
import Image from "next/image";

function LoginForm() {
  const t = useTranslations("login");
  const [key, setKey] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const router = useRouter();
  const searchParams = useSearchParams();

  // Validate returnTo to prevent open-redirect via crafted login links.
  const returnTo = sanitizeReturnTo(searchParams.get("returnTo"));

  useEffect(() => {
    // Focus the input on mount
    const input = document.getElementById("access-key");
    input?.focus();
  }, []);

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    setError("");
    setLoading(true);

    try {
      const res = await fetch(withBasePath("/api/auth/login"), {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ key }),
      });

      if (res.ok) {
        // An operator session names no schema, so the API answers with the page
        // that can act (the tenant list) rather than letting the dashboard
        // bounce on its first unauthorized request.
        const data = (await res.json().catch(() => null)) as { landing?: string } | null;
        router.push(data?.landing ?? returnTo);
        router.refresh();
      } else {
        const data = await res.json().catch(() => null);
        setError(data?.error || t("invalidKey"));
      }
    } catch {
      setError(t("connectFailed"));
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="relative flex min-h-screen items-center justify-center overflow-hidden bg-background p-4">
      {/* Atmosphere: a blueprint grid for depth, an aurora bloom on top. Both
          are inert decoration — no pointer events, no layout effect. */}
      <div aria-hidden className="texture-grid pointer-events-none absolute inset-0 opacity-70" />
      <div
        aria-hidden
        className="texture-aurora animate-aurora-drift pointer-events-none absolute inset-x-0 top-0 h-[520px]"
      />
      <div
        aria-hidden
        className="pointer-events-none absolute inset-x-0 bottom-0 h-px bg-gradient-to-r from-transparent via-primary/25 to-transparent"
      />

      <div className="absolute top-4 right-4 z-10">
        <LanguageSwitcher />
      </div>

      <div className="animate-rise-in panel-edge relative w-full max-w-[404px] rounded-[18px] border border-border border-solid bg-card p-8">
        <div className="flex flex-col items-center text-center">
          <div className="relative mb-5 flex h-[92px] w-[92px] items-center justify-center rounded-[22px] border border-primary/15 bg-primary/[0.04]">
            <div
              aria-hidden
              className="pointer-events-none absolute inset-0 rounded-[22px] shadow-[inset_0_1px_0_0_rgba(255,255,255,0.35),0_0_0_6px_color-mix(in_oklab,var(--primary)_7%,transparent)]"
            />
            <Image
              // The 512px master, not `favicon.png`: this plate is the largest
              // the mark is ever shown, so it should not upscale on a 2x display.
              src={withBasePath("/logo-512.png")}
              alt=""
              width={64}
              height={64}
              unoptimized
              className="relative h-16 w-16 select-none object-contain"
            />
          </div>

          <Eyebrow>control plane</Eyebrow>
          <h1 className="font-heading text-[22px] leading-tight font-semibold tracking-[-0.022em] text-foreground">
            {t("title")}
          </h1>
          <p className="mt-2 text-[13px] leading-relaxed text-muted-foreground">
            {t("description")}
          </p>
        </div>

        <form onSubmit={handleSubmit} className="mt-7 space-y-4">
          <div className="space-y-1.5">
            <label
              htmlFor="access-key"
              className="font-mono text-[10.5px] font-medium uppercase tracking-[0.11em] text-muted-foreground"
            >
              {t("accessKey")}
            </label>
            <Input
              id="access-key"
              type="password"
              placeholder={t("accessKeyPlaceholder")}
              value={key}
              onChange={(e) => setKey(e.target.value)}
              autoComplete="off"
              className="h-10"
            />
          </div>

          {error && (
            <p
              role="alert"
              className="flex items-start gap-2 rounded-[9px] border border-destructive/25 bg-destructive/[0.07] px-3 py-2 text-[12.5px] leading-relaxed text-destructive"
            >
              <AlertCircle className="mt-px h-3.5 w-3.5 shrink-0" />
              <span>{error}</span>
            </p>
          )}

          <Button type="submit" variant="gradient" size="lg" className="group w-full" disabled={loading || !key}>
            {loading ? t("signingIn") : t("signIn")}
            {!loading && (
              <ArrowRight className="h-4 w-4 transition-transform duration-200 ease-[var(--ease-out-quint)] group-hover:translate-x-0.5" />
            )}
          </Button>
        </form>
      </div>
    </div>
  );
}

export default function LoginPage() {
  return (
    <Suspense>
      <LoginForm />
    </Suspense>
  );
}