/**
 * SlotMap merge for the two **Plugins page** seats this plugin registers into.
 *
 * `plugins.item` and `plugins.bundle.config` are declared at runtime by the
 * harness's plugin-manager client bundle, not by any published type package —
 * and this plugin deliberately does not depend on that bundle: a headless
 * consumer must not drag in the browser UI stack, and a client bundle may only
 * require the shell's platform singletons anyway.
 *
 * Without this merge `ctx.slots.inject('plugins.item', …)` is a type error,
 * because `SlotMap` is only as wide as the type-only packages imported here.
 * The declarations transcribe the page's own table, key-typed only, so a
 * list/keyed mix-up fails at registration instead of silently in the browser.
 * @module @studyzy/dsh-wenyuan-memory/plugins-page-slots
 */

export {}

declare module '@deepseek-ai/dsh-client-ui-slots' {
  interface SlotMap {
    /**
     * The Plugins page's card list. The page renders one entry twice:
     * `view: 'summary'` as the card's one-liner, `view: 'page'` as the body of
     * the plugin's own detail page. Root-scoped: the list exists once per app.
     */
    'plugins.item': {
      kind: 'list'
      scope: 'root'
    }
    /**
     * One bundle's configuration section on its **package** detail page.
     *
     * Keyed, and addressed by the bundle's *package* name: the page computes
     * `configured` from the entries' `options.key`, so a key of anything but
     * the npm package name (or an `id`, which a keyed slot does not read)
     * registers without error and then never matches — the section silently
     * never renders.
     */
    'plugins.bundle.config': {
      kind: 'keyed'
      scope: 'root'
    }
  }
}