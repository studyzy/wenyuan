import { cookies, headers } from "next/headers";

import { negotiateLocale } from "@/i18n/negotiate";
import { buildManifest } from "@/lib/manifest";

/**
 * GET /manifest.webmanifest
 *
 * A route handler rather than `app/manifest.ts`: the framework's manifest file
 * is generated once at build time, so it cannot answer in the session's
 * language. The negotiated locale is resolved exactly as the pages resolve it —
 * the locale cookie pair, then Accept-Language, then the default — so the
 * manifest a browser installs the app with matches the UI it opens.
 *
 * The middleware matcher skips `.webmanifest`, so this request never enters the
 * locale rewrite; it must stay in `PUBLIC_PATH_PREFIXES` so the gate does not
 * redirect it to /login (an install-time fetch would otherwise fail to parse).
 */
export async function GET(): Promise<Response> {
  const cookieHeader = (await cookies())
    .getAll()
    .map((cookie) => `${cookie.name}=${cookie.value}`)
    .join("; ");
  const acceptLanguage = (await headers()).get("accept-language") ?? undefined;
  const manifest = buildManifest(negotiateLocale({ cookieHeader, acceptLanguage }));

  return new Response(JSON.stringify(manifest), {
    headers: { "content-type": "application/manifest+json" },
  });
}
