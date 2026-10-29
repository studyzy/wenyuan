/**
 * The staged form behind the WenYuan memory card.
 *
 * The card edits three settings fields through the shared `SettingsFormModel`,
 * which stages what the user types and writes only on save. The API key is the
 * one control outside the section: a secret literal must never ride a settings
 * response, so the card writes it through the credentials domain (which answers
 * only "configured" or not) and re-reads that answer afterwards.
 *
 * A URL field is validated here rather than by the Host: a settings section
 * accepts any string, and a typo there would silently point every memory call
 * at nothing.
 * @module @studyzy/dsh-wenyuan-memory/settings-controller
 */

import type { Context as ClientContext } from '@deepseek-ai/cordis'
// Type-only: pulls the `ctx.remote` / `ctx.remote.credentials` merges.
import type {} from '@deepseek-ai/dsh-api-remotes/client'
import { createSnapshotStore, type SnapshotStore } from '@deepseek-ai/dsh-client-store'
import {
  SettingsFormModel, settingsTextField,
  type SettingsFieldSpec, type SettingsFieldState, type SettingsFormActions, type SettingsFormShell,
  type SettingsFormScope,
} from '@deepseek-ai/dsh-client-ui-primitives'
import {
  API_KEY_REF, DEFAULT_BANK_ID, DEFAULT_API_URL, WENYUAN_MEMORY_NS,
  type WenYuanMemorySettings,
} from '../settings.ts'

export { WENYUAN_MEMORY_NS }

/** Form field the credential control stages under. */
const API_KEY_FIELD = 'apiKey'

/** The section fields this card edits, in render order. */
const SECTION_FIELDS: readonly string[] = ['apiUrl', 'bankId', 'injectContext', 'retainOnStop']

/** What the credentials domain last reported. */
interface CredentialState {
  /** Whether a value is stored for the reference. */
  configured: boolean
  /** Whether the credentials domain accepts a write; false disables the control. */
  writable: boolean
}

/** What the card renders. */
export interface WenYuanMemoryCardState extends SettingsFormShell {
  /** Base URL field. */
  apiUrl: SettingsFieldState
  /** Memory bank field. */
  bankId: SettingsFieldState
  /** Attach-memory switch. */
  injectContext: SettingsFieldState
  /** Write-back switch. */
  retainOnStop: SettingsFieldState
  /** The staged credential, blank on every load. */
  apiKey: SettingsFieldState
  /** Whether the Host reports a key configured for the reference. */
  apiKeyConfigured: boolean
  /** Whether the credentials domain accepts a write; false disables the control. */
  apiKeyWritable: boolean
}

/** The registration-side face the card's slot entries inject. */
export interface WenYuanMemoryCardFace extends SettingsFormActions {
  hooks: {
    /** Card snapshot bound by the renderer as useWenYuanMemoryCard. */
    wenYuanMemoryCard: SnapshotStore<WenYuanMemoryCardState>
  }
  /** Stage one switch position. */
  setSwitch: (field: 'injectContext' | 'retainOnStop', enabled: boolean) => void
}

/**
 * A URL field: an http(s) address, or a blank that clears back to the default.
 * @param field - field name inside the namespace section.
 * @returns the field's conversion spec.
 */
function settingsUrlField(field: string): SettingsFieldSpec {
  return {
    field,
    format: (value) => (typeof value === 'string' ? value : ''),
    parse: (text) => {
      const trimmed = text.trim()
      if (trimmed === '') return { kind: 'clear' }
      return /^https?:\/\/\S+$/i.test(trimmed) ? { kind: 'set', value: trimmed } : undefined
    },
  }
}

/** A boolean switch: the stored value as `true`/`false` text. */
function settingsSwitchField(field: string): SettingsFieldSpec {
  return {
    field,
    format: (value) => (value === undefined ? '' : String(value === true)),
    parse: (text) => ({ kind: 'set', value: text !== 'false' }),
  }
}

/** Bridges the entry's settings scope and the credentials domain onto the card. */
export class WenYuanMemoryCardController {
  private readonly form: SettingsFormModel<WenYuanMemorySettings>
  private readonly store: SnapshotStore<WenYuanMemoryCardState>
  private readonly unsubscribe: Array<() => void>
  private credential: CredentialState = { configured: false, writable: true }

  /**
   * @param scope - the shared configuration form for this card's namespace.
   * @param ctx - the card plugin's context; its `remote.credentials` namespace
   * answers for the API key.
   */
  constructor(
    scope: SettingsFormScope<WenYuanMemorySettings>,
    private readonly ctx: ClientContext,
  ) {
    this.form = new SettingsFormModel<WenYuanMemorySettings>(scope, [
      settingsUrlField('apiUrl'),
      settingsTextField('bankId'),
      settingsSwitchField('injectContext'),
      settingsSwitchField('retainOnStop'),
    ], [{ field: API_KEY_FIELD, write: (text) => this.writeKey(text) }])
    this.store = createSnapshotStore(this.projection())
    this.unsubscribe = [
      this.form.bind(() => this.form.shell()).subscribe(() => { this.publish() }),
      scope.subscribe(() => { void this.readCredential() }),
    ]
    void this.readCredential()
  }

  /** Build the face the card's slot entries inject. */
  inject(): WenYuanMemoryCardFace {
    const actions = this.form.actions()
    const refresh = (): void => { this.publish() }
    return {
      hooks: { wenYuanMemoryCard: this.store },
      edit: (field, text) => { actions.edit(field, text); refresh() },
      resetField: (field) => { actions.resetField(field); refresh() },
      save: () => { void this.save() },
      discard: () => { actions.discard(); refresh() },
      setSwitch: (field, enabled) => { actions.edit(field, String(enabled)); refresh() },
    }
  }

  /**
   * Whether a staged URL would be refused. A blank is not an error — saving it
   * clears back to the default — but anything that is not an http(s) address
   * is, and it must block the save rather than point every memory call at
   * nothing.
   * @param text - the staged draft.
   * @returns true when the draft is present and unusable.
   */
  private static urlInvalid(text: string): boolean {
    const trimmed = text.trim()
    return trimmed !== '' && !/^https?:\/\/\S+$/i.test(trimmed)
  }

  /** Publish the card projection. */
  private projection(): WenYuanMemoryCardState {
    const shell = this.form.shell()
    const apiUrl = this.form.field('apiUrl')
    const urlInvalid = WenYuanMemoryCardController.urlInvalid(apiUrl.text)
    return {
      ...shell,
      invalid: shell.invalid || urlInvalid,
      apiUrl: { ...apiUrl, invalid: urlInvalid },
      bankId: this.form.field('bankId'),
      injectContext: this.form.field('injectContext'),
      retainOnStop: this.form.field('retainOnStop'),
      apiKey: { text: this.form.field(API_KEY_FIELD).text, overridden: false, invalid: false },
      apiKeyConfigured: this.credential.configured,
      apiKeyWritable: this.credential.writable,
    }
  }

  /**
   * Ask the credentials domain whether the key is configured.
   *
   * The answer is a presence fact, never the value: that is the whole point of
   * addressing a secret by reference.
   */
  private async readCredential(): Promise<void> {
    const response = await this.ctx.remote.credentials.describe([API_KEY_REF])
    if (!response.ok) return
    const view = response.value[API_KEY_REF]
    const next: CredentialState = {
      configured: view?.configured ?? false,
      // An unknown reference stays writable: the Host is what refuses, rather
      // than the page guessing a refusal.
      writable: view?.writable ?? true,
    }
    if (next.configured === this.credential.configured && next.writable === this.credential.writable) return
    this.credential = next
    this.publish()
  }

  /**
   * Write the staged key, then re-read whether the Host holds one.
   * @param value - the staged credential literal.
   * @returns whether the Host reports a configured credential afterwards.
   */
  private async writeKey(value: string): Promise<boolean> {
    await this.ctx.remote.credentials.set(API_KEY_REF, value)
    await this.readCredential()
    return this.credential.configured
  }

  /** Publish the card projection. */
  private publish(): void {
    this.store.set(this.projection())
  }

  /** Save the section, then the credential the staged form carries. */
  private async save(): Promise<void> {
    const state = this.projection()
    if (!state.available || state.invalid || state.saving) return
    await this.form.save()
    this.publish()
  }

  /** Release the scope subscription and the form. */
  dispose(): void {
    for (const off of this.unsubscribe) off()
    this.unsubscribe.length = 0
    this.form.dispose()
  }

  /** The fields this card edits, for a verification script or a test. */
  static readonly sectionFields = SECTION_FIELDS
  /** The built-in defaults the card's controls revert to. */
  static readonly defaults = { apiUrl: DEFAULT_API_URL, bankId: DEFAULT_BANK_ID }
}