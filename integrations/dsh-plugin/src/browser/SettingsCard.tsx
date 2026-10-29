/**
 * The WenYuan memory card on the WebUI Plugins page: the server URL, the memory
 * bank, the API key, and the two behaviour switches, staged until save.
 *
 * The page renders the same component through **three** surfaces:
 *
 * - `plugins.item` with `view: 'summary'` — the one-liner on the plugin's card;
 * - `plugins.item` with `view: 'page'` — the form on that card's detail page;
 * - `plugins.bundle.config` with `view: 'page'` — the form on the installed
 *   package's detail page.
 *
 * The summary branch is a separate, hook-free component rather than an early
 * return: an early return would change the hook count between renders, which
 * React rejects outright.
 * @module @studyzy/dsh-wenyuan-memory/settings-card
 */

import { SettingsForm, SettingsSecretField, SettingsValueField, Switch } from '@deepseek-ai/dsh-client-ui-primitives'
import type { InjectFace, PropsLocale, PropsRuntime } from '@deepseek-ai/dsh-client-ui-slots'
// Type-only: pulls the LocaleNamespaceMap / SlotMap merges this card's props depend on.
import type {} from '@deepseek-ai/dsh-client-ui-settings/client'
import type {} from '../browser/settings-locales.ts'
import type { WenYuanMemoryCardFace, WenYuanMemoryCardState } from './settings-controller.ts'

/**
 * The view the Plugins page asks one configuration entry for.
 */
export interface PluginConfigViewProps {
  /** `summary` renders the one-liner alone; `page` renders the configuration form. */
  readonly view?: 'summary' | 'page' | undefined
}

/** Props the renderer binds for the WenYuan memory card. */
export type SettingsCardProps =
  PluginConfigViewProps
  // Both seats are root-scoped slots with no owner props, so the standard-kit
  // share is empty here; the root-scoped, owner-less settings entry is named.
  & PropsRuntime<'settings.plugins.tab'>
  & PropsLocale<'wenyuan-memory.settings'>
  & InjectFace<WenYuanMemoryCardFace>

/** The card's one-liner, with no hooks of its own. */
function Summary(props: SettingsCardProps) {
  const { t } = props
  return <p>{t('description')}</p>
}

/** The card's configuration form. */
function Configuration(props: SettingsCardProps) {
  const { t } = props
  // The renderer binds each injected snapshot store as `use<StoreName>`; the
  // store this card declares is `wenYuanMemoryCard`.
  const state: WenYuanMemoryCardState = props.useWenYuanMemoryCard(
    (snapshot: WenYuanMemoryCardState) => snapshot,
  )
  const disabled = !state.writable || state.saving
  return (
    <SettingsForm
      labels={{
        unavailable: t('unavailable'),
        readOnly: t('readOnly'),
        saveFailed: t('saveFailed'),
        save: t('save'),
        saving: t('saving'),
      }}
      state={state}
      onSave={props.save}
      onDiscard={props.discard}
    >
      <SettingsValueField
        id="wenyuan-memory-api-url"
        label={t('apiUrl')}
        hint={t('apiUrlHint')}
        overriddenLabel={t('overridden')}
        resetLabel={t('reset')}
        invalidLabel={t('invalidUrl')}
        disabled={disabled}
        {...state.apiUrl}
        onEdit={(text) => { props.edit('apiUrl', text) }}
        onReset={() => { props.resetField('apiUrl') }}
      />
      <SettingsValueField
        id="wenyuan-memory-bank-id"
        label={t('bankId')}
        hint={t('bankIdHint')}
        overriddenLabel={t('overridden')}
        resetLabel={t('reset')}
        invalidLabel={t('invalidUrl')}
        disabled={disabled}
        {...state.bankId}
        onEdit={(text) => { props.edit('bankId', text) }}
        onReset={() => { props.resetField('bankId') }}
      />
      <SettingsSecretField
        id="wenyuan-memory-api-key"
        label={t('apiKey')}
        hint={t('apiKeyHint')}
        // A key can be written even when the settings document is read-only:
        // they are separate stores with separate refusals, and this control's
        // own writability is what disables it.
        disabled={!state.apiKeyWritable}
        text={state.apiKey.text}
        configured={state.apiKeyConfigured}
        stateLabel={state.apiKeyConfigured ? t('apiKeySet') : t('apiKeyUnset')}
        onEdit={(text) => { props.edit('apiKey', text) }}
      />
      <label>
        <span>{t('injectContext')}</span>
        <Switch
          checked={state.injectContext.text === 'true'}
          label={t('injectContext')}
          disabled={disabled}
          onChange={(checked) => { props.setSwitch('injectContext', checked) }}
        />
      </label>
      <p>{t('injectContextHint')}</p>
      <label>
        <span>{t('retainOnStop')}</span>
        <Switch
          checked={state.retainOnStop.text === 'true'}
          label={t('retainOnStop')}
          disabled={disabled}
          onChange={(checked) => { props.setSwitch('retainOnStop', checked) }}
        />
      </label>
      <p>{t('retainOnStopHint')}</p>
    </SettingsForm>
  )
}

/**
 * Render the card's one-liner or its configuration form, as the page asks.
 * @param props - the view asked for, locale copy, the form snapshot, and its actions.
 * @returns the one-liner, or the form.
 */
export function SettingsCard(props: SettingsCardProps) {
  return props.view === 'summary' ? <Summary {...props} /> : <Configuration {...props} />
}