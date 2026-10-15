import { Inter, JetBrains_Mono } from "next/font/google";
import { NextIntlClientProvider, hasLocale } from "next-intl";
import { setRequestLocale } from "next-intl/server";
import { notFound } from "next/navigation";
import { routing } from "@/i18n/routing";
import { BankProvider } from "@/lib/bank-context";
import { FeaturesProvider } from "@/lib/features-context";
import { ThemeProvider } from "@/lib/theme-context";
import { Toaster } from "@/components/ui/sonner";
import { themeBootstrapScript } from "@/app/providers/theme-bootstrap";

// Inter preloaded server-side so every weight (400/500/600/700) is ready
// before paint. The Google Fonts @import this replaces loaded weights lazily,
// which made semibold headings render in the system fallback until the weight
// arrived. --font-sans / --font-mono are consumed by globals.css.
const inter = Inter({
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
      className={`${inter.variable} ${jetbrainsMono.variable}`}
      suppressHydrationWarning
    >
      <body className="bg-background text-foreground">
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