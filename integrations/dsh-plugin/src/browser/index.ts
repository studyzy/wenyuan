/**
 * WenYuan memory, browser half: the configuration card on the WebUI Plugins
 * page.
 *
 * The page files this package twice — as an official-plugin card and, because
 * the bundle is also a profile dependency, as an installed package card — so
 * the card registers into **both** seats. They share ONE face (`const face =
 * controller.inject()`): each registration's `inject` callback runs per
 * surface, so a fresh face per surface would give each its own draft table and
 * a save on the stale page would write its own empty draft back.
 *
 * The card is an enhancement over the memory itself, which runs in the Host
 * half; it mounts on a scoped inject, so a deployment without the settings
 * surface still gets memory.
 * @module @studyzy/dsh-wenyuan-memory/browser
 */

import type { Context as ClientContext } from '@deepseek-ai/cordis'
// Type-only: declares the two Plugins-page seats this plugin registers into.
import type {} from '../browser/plugins-page-slots.ts'
// Type-only: the ctx.configForms service merge, plus the ctx.slots merge the renderer supplies.
import type {} from '@deepseek-ai/dsh-client-ui-settings/client'
import type {} from '@deepseek-ai/dsh-client-ui-renderer/client'
// Type-only: the ctx.locale Context merge.
import type {} from '@deepseek-ai/dsh-client-locale/client'
import type {} from '@deepseek-ai/dsh-api-remotes/client'
import { SettingsCard } from './SettingsCard.tsx'
import { WenYuanMemoryCardController, WENYUAN_MEMORY_NS } from './settings-controller.ts'
import type { WenYuanMemorySettings } from '../settings.ts'
import { en, zh } from './settings-locales.ts'

export { SettingsCard } from './SettingsCard.tsx'
export { WenYuanMemoryCardController, WENYUAN_MEMORY_NS } from './settings-controller.ts'
export type {
  WenYuanMemoryCardFace, WenYuanMemoryCardState,
} from './settings-controller.ts'

/** Locale namespace of the card copy. */
const NS = 'wenyuan-memory.settings'

/**
 * This package's name: the key the bundle configuration registers under.
 *
 * `plugins.bundle.config` is a **keyed** slot addressed by the npm package name,
 * not by the settings namespace. A namespace key registers without erroring and
 * then never matches, so the configuration section silently never renders.
 * Spelled as a literal because the client bundle is a browser artifact and
 * pulling the manifest in would inline it; `scripts/verify-client-bundle.mjs`
 * cross-checks this constant against `package.json`.
 */
const PACKAGE_NAME = '@studyzy/dsh-wenyuan-memory'

/** The official-plugins card seat: a root-scoped list slot. */
const PLUGINS_ITEM = 'plugins.item'

/** The installed-package configuration seat: a root-scoped keyed slot. */
const BUNDLE_CONFIG = 'plugins.bundle.config'

/** Registration order of this card among the contributed cards. */
const ITEM_ORDER = 40

/** Required services: the slot registry, locale copy, and the shared config forms. */
export const inject = ['slots']

/**
 * Client plugin body: the configuration card on both Plugins-page surfaces.
 * @param ctx - client root context.
 */
export function apply(ctx: ClientContext): void {
  // `remote.credentials` is its own injectable service even though it hangs off
  // `remote`: Cordis resolves a context property only when the fiber's `inject`
  // names it, so without the sub-namespace the card's write throws
  // `cannot get property "remote.credentials" without inject` and the save can
  // only report a generic failure. The official settings pages declare it the
  // same way (`ui-settings-models`, `ui-settings-web-search`).
  ctx.inject(['configForms', 'locale', 'remote', 'remote.credentials'], (settingsCtx) => {
    settingsCtx.effect(
      () => settingsCtx.locale.register(NS, { zh, en }),
      'ui-wenyuan-memory: settings card dictionary',
    )
    // Bound once here, so registration-time text and the injected face share
    // one translate and copy freshness rides the locale revision.
    const t = settingsCtx.locale.bind(NS)
    const controller = new WenYuanMemoryCardController(
      settingsCtx.configForms.get<WenYuanMemorySettings>(WENYUAN_MEMORY_NS),
      settingsCtx,
    )
    settingsCtx.effect(() => () => { controller.dispose() }, 'ui-wenyuan-memory: form observers')

    // ONE face for BOTH surfaces: each registration's `inject` runs per surface,
    // so a fresh face per call would split the staged-edit table in two.
    const face = controller.inject()

    settingsCtx.slots.inject(PLUGINS_ITEM, () => settingsCtx.slots.register({
      name: PLUGINS_ITEM,
      id: WENYUAN_MEMORY_NS,
      order: ITEM_ORDER,
      // Slot labels are strings or thunks; the page resolves the thunk against
      // the locale revision so the card re-labels on switch.
      label: () => t('title'),
      locale: NS,
      inject: () => face,
    }, SettingsCard))

    // Keyed by package name, page view only.
    settingsCtx.slots.inject(BUNDLE_CONFIG, () => settingsCtx.slots.register({
      name: BUNDLE_CONFIG,
      key: PACKAGE_NAME,
      locale: NS,
      inject: () => face,
    }, SettingsCard))
  })
}