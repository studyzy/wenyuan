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
];

export default config;
