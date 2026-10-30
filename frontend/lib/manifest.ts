import type { MetadataRoute } from "next";

import { getCatalog, lookupMessage } from "@/i18n/catalog";
import type { Locale } from "@/i18n/locales";

/**
 * The web app manifest for one locale.
 *
 * Only the two brand-bearing fields are localized: `name` is the app's full
 * name ("文渊控制台" / "WenYuan Control Plane", the same string the browser tab
 * shows) and `short_name` is the wordmark the install prompt and home screen
 * use ("文渊" / "WenYuan"). Everything else — the start URL, the display mode,
 * the colors, the icons — is locale-independent and stays byte-identical across
 * locales.
 */
export function buildManifest(locale: Locale): MetadataRoute.Manifest {
  const catalog = getCatalog(locale);
  return {
    name: lookupMessage(catalog, "common.appName"),
    short_name: lookupMessage(catalog, "common.brandName"),
    description: "Control plane for the temporal semantic memory system",
    start_url: "/dashboard",
    display: "standalone",
    // Matches --background (light) and --primary in app/globals.css.
    background_color: "#F4F6FB",
    theme_color: "#0052D9",
    icons: [
      { src: "/icon-192.png", sizes: "192x192", type: "image/png", purpose: "any" },
      { src: "/icon-512.png", sizes: "512x512", type: "image/png", purpose: "any" },
    ],
  };
}
