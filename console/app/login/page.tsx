import { cookies } from "next/headers";
import { buildLoginViewModel } from "../../lib/login-view";
import { negotiateLocale } from "../../i18n/negotiate";
import { getCatalog, lookupMessage } from "../../i18n/catalog";
import { LoginForm } from "./login-form";

/**
 * Login page.
 *
 * Whitelisted page path accepting a returnTo query parameter. Offers a language
 * switcher so the locale can change before authentication.
 */
export default async function LoginPage({
  searchParams,
}: {
  searchParams: Promise<{ returnTo?: string; locale?: string }>;
}) {
  const resolvedSearchParams = await searchParams;
  const cookieHeader = (await cookies())
    .getAll()
    .map((cookie: { name: string; value: string }) => `${cookie.name}=${cookie.value}`)
    .join("; ");
  const locale = resolvedSearchParams.locale ?? negotiateLocale({ cookieHeader });
  const view = buildLoginViewModel({ returnTo: resolvedSearchParams.returnTo, locale });
  const catalog = getCatalog(view.locale);

  return (
    <main className="console-surface">
      <h1>{lookupMessage(catalog, "auth.signInTitle")}</h1>
      <p>{lookupMessage(catalog, "auth.signInSubtitle")}</p>
      <LoginForm
        returnTo={view.returnTo}
        locale={view.locale}
        languages={[...view.languages]}
        labels={{
          accessKey: lookupMessage(catalog, "auth.accessKeyLabel"),
          signIn: lookupMessage(catalog, "auth.signIn"),
          language: lookupMessage(catalog, "nav.language"),
        }}
      />
    </main>
  );
}