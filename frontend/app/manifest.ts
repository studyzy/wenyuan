import type { MetadataRoute } from "next";

/**
 * The web app manifest.
 *
 * Served by the framework as a route rather than dropped in `public/` as a
 * static file: Next does not serve the `.webmanifest` extension out of
 * `public/`, so a static copy 404s. A route also lets `theme_color` track the
 * brand token in one place.
 */
export default function manifest(): MetadataRoute.Manifest {
  return {
    name: "WenYuan Control Plane",
    short_name: "WenYuan",
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
