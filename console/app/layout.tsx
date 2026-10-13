import type { Metadata } from "next";
import type { ReactNode } from "react";
import { cookies, headers } from "next/headers";
import { Providers } from "./providers";
import { themeBootstrapScript } from "./providers/theme-bootstrap";
import { negotiateLocale } from "../i18n/negotiate";
import { getCatalog, lookupMessage } from "../i18n/catalog";
import { NextIntlClientProvider } from "next-intl";
import "./globals.css";

/**
 * Document root. Renders the document with a lang attribute matching the
 * resolved locale, installs the client providers, applies the pre-paint theme
 * bootstrap, and mounts the notification surface (inside Providers).
 */
export async function generateMetadata(): Promise<Metadata> {
  const locale = await resolveLocale();
  const catalog = getCatalog(locale);
  return {
    title: lookupMessage(catalog, "common.appName"),
    description: lookupMessage(catalog, "common.appName"),
  };
}

async function resolveLocale() {
  const cookieHeader = (await cookies())
    .getAll()
    .map((cookie: { name: string; value: string }) => `${cookie.name}=${cookie.value}`)
    .join("; ");
  const acceptLanguage = (await headers()).get("accept-language") ?? undefined;
  return negotiateLocale({ cookieHeader, acceptLanguage });
}

export default async function RootLayout({ children }: { children: ReactNode }) {
  const locale = await resolveLocale();
  const catalog = getCatalog(locale);

  return (
    <html lang={locale} suppressHydrationWarning>
      <head>
        <script dangerouslySetInnerHTML={{ __html: themeBootstrapScript() }} />
      </head>
      <body>
        <NextIntlClientProvider locale={locale} messages={catalog}>
          <Providers>{children}</Providers>
        </NextIntlClientProvider>
      </body>
    </html>
  );
}