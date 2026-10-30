import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "WenYuan Control Plane",
  description: "Control plane for the temporal semantic memory system",
  // `favicon.png` is the transparent product mark, so it works on either a
  // light or a dark browser chrome. The apple/app tile is the same mark on its
  // opaque dark-blue plate: iOS composites transparency onto black, which would
  // otherwise swallow a dark mark, and the manifest icons want a full-bleed
  // square rather than a transparent one.
  icons: {
    icon: "/favicon.png",
    apple: "/apple-touch-icon.png",
  },
  // `manifest` is not declared here: it is per-locale (the installed app name
  // follows the session language), so `app/[locale]/layout.tsx` declares it.
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  // The root layout is a minimal shell. Locale-aware content,
  // providers, and <html lang> are handled in [locale]/layout.tsx.
  return children;
}