import type { Metadata } from "next";
import { JetBrains_Mono, Manrope, Sora } from "next/font/google";
import { NextIntlClientProvider, hasLocale } from "next-intl";
import { getTranslations, setRequestLocale } from "next-intl/server";
import { notFound } from "next/navigation";
import { routing } from "@/i18n/routing";
import { normalizeBasePath } from "@/lib/base-path";
import { BankProvider } from "@/lib/bank-context";
import { FeaturesProvider } from "@/lib/features-context";
import { ThemeProvider } from "@/lib/theme-context";
import { Toaster } from "@/components/ui/sonner";
import { themeBootstrapScript } from "@/app/providers/theme-bootstrap";

// Sora carries display and heading copy: a geometric, slightly cut-corner
// sans that reads as engineered rather than friendly. Manrope carries the UI
// voice — an open grotesque whose flat terminals stay legible at 13px, where
// Sora's tighter forms would not. Both preload server-side so every weight is
// ready before paint. --font-display / --font-sans / --font-mono are consumed
// by globals.css.
const sora = Sora({
  subsets: ["latin"],
  weight: ["400", "500", "600", "700"],
  variable: "--font-display",
  display: "swap",
});

const manrope = Manrope({
  subsets: ["latin"],
  weight: ["400", "500", "600", "700"],
  variable: "--font-sans",
  display: "swap",
});

const jetbrainsMono = JetBrains_Mono({
  subsets: ["latin"],
  weight: ["400", "500"],
  variable: "--font-mono",
  display: "swap",
});

export function generateStaticParams() {
  return routing.locales.map((locale) => ({ locale }));
}

// The browser tab and the search/social description follow the negotiated
// locale: the root layout's static metadata is English-only, so a Chinese
// session would otherwise title the tab in pinyin. The title is the same
// `common.appName` the web app manifest installs under, so the tab and the
// installed app name never disagree.
export async function generateMetadata({
  params,
}: {
  params: Promise<{ locale: string }>;
}): Promise<Metadata> {
  const { locale } = await params;
  const [common, meta] = await Promise.all([
    getTranslations({ locale, namespace: "common" }),
    getTranslations({ locale, namespace: "metadata" }),
  ]);
  return {
    title: common("appName"),
    description: meta("description"),
    // The manifest is a route handler (app/manifest.webmanifest/route.ts) so it
    // can answer in the session's language; the framework only emits the <link>
    // for its own app/manifest.ts special file, so it is declared here.
    manifest: `${normalizeBasePath()}/manifest.webmanifest`,
  };
}

// Anti-FOUC: apply the theme's `.dark` class before first paint. ThemeProvider
// only reads localStorage/system preference in a useEffect (after paint), so
// without this a dark-mode user gets a light flash on every hard refresh. This
// runs synchronously as the first <body> child, before the content below it
// paints. The snippet is derived from the frontend theme module so the shell and
// its test agree on the storage key and the logic.
const THEME_INIT_SCRIPT = themeBootstrapScript();

export default async function LocaleLayout({
  children,
  params,
}: {
  children: React.ReactNode;
  params: Promise<{ locale: string }>;
}) {
  const { locale } = await params;
  if (!hasLocale(routing.locales, locale)) {
    notFound();
  }

  setRequestLocale(locale);

  return (
    <html
      lang={locale}
      className={`${sora.variable} ${manrope.variable} ${jetbrainsMono.variable}`}
      suppressHydrationWarning
    >
      <body className="bg-background text-foreground antialiased">
        <script dangerouslySetInnerHTML={{ __html: THEME_INIT_SCRIPT }} />
        <ThemeProvider>
          <FeaturesProvider>
            <BankProvider>
              <NextIntlClientProvider>{children}</NextIntlClientProvider>
            </BankProvider>
          </FeaturesProvider>
        </ThemeProvider>
        <Toaster />
      </body>
    </html>
  );
}