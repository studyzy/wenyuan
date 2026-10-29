/**
 * The published UI kit's form model, reproduced for the model-level tests.
 *
 * The kit's bundled output imports its own renderer closure (`@shikijs/langs`,
 * `anser`, …) at module load; those are the kit's dependencies, not this
 * package's, and a client bundle may not require them. The controller under
 * test is the KIT'S consumer, so the suite substitutes a form model with the
 * same staging contract instead of dragging that closure into the build.
 * @module @studyzy/dsh-wenyuan-memory/tests/helpers/primitives-stub
 */

export const settingsTextField = (field: string) => ({ field, format: (v: unknown) => (typeof v === 'string' ? v : ''), parse: (t: string) => (t === '' ? { kind: 'clear' as const } : { kind: 'set' as const, value: t }) })
export const settingsNumberField = (field: string) => ({ field, format: (v: unknown) => (typeof v === 'number' ? String(v) : ''), parse: (t: string) => (t === '' ? { kind: 'clear' as const } : { kind: 'set' as const, value: Number(t) }) })

/**
 * A faithful stand-in for the kit's staged form model: the same staging,
 * dirty/invalid projection and one revision-fenced save, without the kit's
 * renderer dependency closure (the published bundle imports `@shikijs/langs`,
 * `anser`, … at load, which are the kit's own deps, not this package's).
 */
export class SettingsFormModel<T> {
  private staged = new Map<string, { text: string; clear: boolean }>()
  private listeners = new Set<() => void>()
  private saving = false
  private failed = false
  private readonly off: () => void
  constructor(
    private readonly scope: SettingsFormScope<T>,
    private readonly specs: { field: string; format: (v: unknown) => string; parse: (t: string) => unknown }[],
    private readonly secrets: { field: string; write: (t: string) => Promise<boolean> }[] = [],
  ) {
    this.off = scope.subscribe(() => { this.emit() })
  }

  bind<S>(project: () => S) {
    return {
      getSnapshot: project,
      subscribe: (listener: () => void) => { this.listeners.add(listener); return () => { this.listeners.delete(listener) } },
    }
  }

  shell() {
    const snapshot = this.scope.getSnapshot()
    let invalid = false
    for (const [field, edit] of this.staged) {
      const spec = this.specs.find((candidate) => candidate.field === field)
      if (spec !== undefined && !edit.clear && spec.parse(edit.text) === undefined) invalid = true
    }
    return {
      available: snapshot.status === 'ready',
      writable: snapshot.writable,
      dirty: this.staged.size > 0,
      invalid,
      saving: this.saving,
      failed: this.failed,
    }
  }

  field(field: string) {
    const staged = this.staged.get(field)
    if (staged !== undefined) {
      return { text: staged.clear ? '' : staged.text, overridden: !staged.clear, invalid: false }
    }
    const snapshot = this.scope.getSnapshot()
    const value = (snapshot.value ?? {}) as Record<string, unknown>
    const user = (snapshot.user ?? {}) as Record<string, unknown>
    const spec = this.specs.find((candidate) => candidate.field === field)
    return {
      text: spec === undefined ? '' : spec.format(value[field]),
      overridden: Object.hasOwn(user, field),
      invalid: false,
    }
  }

  actions() {
    return {
      edit: (field: string, text: string) => { this.staged.set(field, { text, clear: false }); this.failed = false; this.emit() },
      resetField: (field: string) => { this.staged.set(field, { text: '', clear: true }); this.emit() },
      save: () => { void this.save() },
      discard: () => { this.staged.clear(); this.failed = false; this.emit() },
    }
  }

  async save() {
    if (this.staged.size === 0 || this.saving) return
    this.saving = true
    this.emit()
    // Mirrors the real `SettingsFormModel.save`: a secret write that throws is
    // caught and marks the save failed, so a refused credential never escapes
    // as an unhandled rejection.
    try {
      const ops: unknown[] = []
      for (const [field, edit] of this.staged) {
        if (edit.clear) { ops.push({ op: 'unset', path: [field] }) ; continue }
        const secret = this.secrets.find((candidate) => candidate.field === field)
        if (secret !== undefined) { if (!await secret.write(edit.text)) this.failed = true; continue }
        const spec = this.specs.find((candidate) => candidate.field === field)
        const write = spec?.parse(edit.text) as { kind: 'set'; value: unknown } | { kind: 'clear' } | undefined
        if (write === undefined) { this.failed = true; continue }
        ops.push(write.kind === 'clear' ? { op: 'unset', path: [field] } : { op: 'set', path: [field], value: write.value })
      }
      if (ops.length > 0) {
        const accepted = await this.scope.mutate(ops as never, this.scope.getSnapshot().revision)
        if (accepted) this.staged.clear()
        else this.failed = true
      }
    } catch {
      this.failed = true
    }
    this.saving = false
    this.emit()
  }

  dispose() { this.off(); this.listeners.clear() }

  private emit() { for (const listener of this.listeners) listener() }
}

