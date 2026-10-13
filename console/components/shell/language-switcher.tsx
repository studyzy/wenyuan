"use client";

import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { Languages } from "lucide-react";

import { SUPPORTED_LOCALES } from "../../i18n";
import { useLocale } from "../../i18n";
import { cn } from "../../lib/utils";

/**
 * Locale picker.
 *
 * The locale is never a URL segment, so switching writes the preference and
 * re-renders in place; the current path and query are preserved. The preference
 * is carried in a cookie the server reads on the next request.
 */
export function LanguageSwitcher({ className }: { className?: string }) {
  const locale = useLocale();
  const pathname = usePathname();
  const searchParams = useSearchParams();
  const router = useRouter();

  const switchTo = (next: string) => {
    document.cookie = `NEXT_LOCALE=${encodeURIComponent(next)};path=/;max-age=31536000`;
    const query = searchParams?.toString();
    router.replace(query ? `${pathname}?${query}` : (pathname ?? "/"));
  };

  return (
    <label className={cn("relative inline-flex items-center gap-2", className)}>
      <Languages className="h-4 w-4 text-muted-foreground" />
      <select
        value={locale}
        onChange={(event) => switchTo(event.target.value)}
        aria-label="Language"
        className="h-8 cursor-pointer rounded-[10px] border border-border bg-card px-2 text-[13px] transition-colors hover:border-primary/40 focus-visible:border-primary focus-visible:outline-none"
      >
        {SUPPORTED_LOCALES.map((value) => (
          <option key={value} value={value}>
            {value}
          </option>
        ))}
      </select>
    </label>
  );
}