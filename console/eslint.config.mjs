import next from "eslint-config-next";

/**
 * Console lint configuration.
 *
 * The console uses the Next.js core-web-vitals rule set on the ESLint flat
 * configuration format required by ESLint 9. eslint-config-next exports a flat
 * config array directly.
 */
const config = [
  ...next,
  {
    ignores: [".next/**", "node_modules/**"],
  },
  {
    // The image component is the wrong tool for the three places the console
    // renders a raw image: the spinner, the agent mark, and inline attachments.
    // Each is a small static or request-time-proxied asset behind server-side
    // auth, so there is no stable URL for an optimiser to resolve and the
    // wrapper element interferes with the transform animations.
    files: ["components/ui/spinner.tsx", "components/ui/harness-logo.tsx", "components/ui/inline-attachment-text.tsx"],
    rules: {
      "@next/next/no-img-element": "off",
    },
  },
];

export default config;