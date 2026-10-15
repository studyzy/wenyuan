import createNextIntlPlugin from "next-intl/plugin";

/**
 * Console build configuration.
 *
 * The console is mounted as a self-contained Next.js application inside an
 * existing repository. The public base path, when configured, is applied to
 * both the served asset prefix and the URL helpers used by the middleware.
 *
 * The next-intl plugin points at the request configuration, which is what lets
 * ported components import next-intl directly; the console's own i18n does not
 * go through it.
 */
const basePath = process.env.NEXT_PUBLIC_BASE_PATH ?? "";

const nextConfig = {
  reactStrictMode: true,
  output: "standalone",
  basePath: basePath || undefined,
  assetPrefix: basePath || undefined,
  env: {
    NEXT_PUBLIC_BASE_PATH: basePath,
  },
};

export default createNextIntlPlugin("./i18n/request.ts")(nextConfig);