import next from "eslint-config-next";

/**
 * Frontend lint configuration.
 *
 * The frontend uses the Next.js core-web-vitals rule set on the ESLint flat
 * configuration format required by ESLint 9. eslint-config-next exports a flat
 * config array directly.
 */
const config = [
  ...next,
  {
    ignores: [".next/**", "node_modules/**"],
  },
  {
    // The image component is the wrong tool for the places the frontend renders
    // a raw image: the spinner, the agent mark, inline attachments, and the
    // spinning logo mark in the bank selector. Each is a small static or
    // request-time-proxied asset, and the wrapper element interferes with the
    // transform animations (the logo mark animates on navigation and clears
    // itself on animationEnd).
    files: [
      "components/ui/spinner.tsx",
      "components/ui/harness-logo.tsx",
      "components/ui/inline-attachment-text.tsx",
      "components/bank-selector.tsx",
    ],
    rules: {
      "@next/next/no-img-element": "off",
    },
  },
  {
    // Modules ported from the reference frontend. Its lint stack registers
    // eslint-plugin-react-hooks but enables none of its rules, so ported code
    // was never held to them; those below are advisory (a synchronous first
    // paint for a polled view, an effect that re-reads a memoised loader, a
    // debounced saver that calls a later-declared helper, a ref that keeps the
    // latest closure for a debounced fetch) and rewriting them at the site
    // would diverge from the reference behaviour we are deliberately keeping.
    // Hand-written modules stay out of this block: the flat view modules at
    // components/ root, the ported locale routes under app/[locale], and this
    // one hook are ports.
    files: ["components/*.tsx", "app/\\[locale\\]/**/*.tsx", "lib/use-refresh-attempts.ts"],
    rules: {
      "react-hooks/set-state-in-effect": "off",
      "react-hooks/exhaustive-deps": "off",
      "react-hooks/immutability": "off",
      "react-hooks/refs": "off",
      // `Date.now()` inside a component body: deliberate and behaviour-
      // preserving. Two views render time-dependent output (an elapsed-time
      // label that ticks every second, and a "deferred until" badge that only
      // means something relative to now); a third uses the clock for a local
      // list key in a click handler. All three are exactly the reference
      // behaviour, and none of them is a stale-read or loop bug, so the rule
      // is exempted rather than the pattern rewritten.
      "react-hooks/purity": "off",
    },
  },
];

export default config;