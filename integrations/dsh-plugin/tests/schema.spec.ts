import { describe, expect, it } from 'vitest'
import { Config, resolveSettings } from '../src/index.ts'
import { DEFAULT_API_URL, DEFAULT_TENANT_NAME } from '../src/settings.ts'

/**
 * The loader schema the Host's settings provider reads.
 *
 * The provider writes one field at a time: `mergeLayers(whole, edits)` layers
 * the edited field over what it read back, and `configEditor.edit` then runs
 * the composed result through the loader schema at `fiber.resolveConfig`. A
 * field declared `required()` fails that step — `$.bankId missing required
 * value` — for any write that reaches the schema without its siblings, and the
 * Plugins page can only report the refusal as "the server rejected the value".
 * A `default()` is folded in exactly when the key is absent, so these cases pin
 * the shape that keeps a partial write valid: defaulted, and still volatile.
 */

/** One field's schemastery meta, as the provider's schema object exposes it. */
interface FieldMeta {
  required?: boolean
  volatile?: boolean
  default?: unknown
}

/** The fields the card edits, in the schema's own order. */
const FIELDS = ['apiUrl', 'tenantName', 'injectContext', 'retainOnStop']

/** The schema's field nodes, read the way the provider reads them. */
function fieldMeta(): Record<string, FieldMeta> {
  const schema = Config as unknown as { type: string; dict: Record<string, { type: string; meta: FieldMeta }> }
  return Object.fromEntries(Object.entries(schema.dict).map(([key, node]) => [key, node.meta]))
}

describe('the loader schema', () => {
  it('declares exactly the four card fields', () => {
    const schema = Config as unknown as { type: string; dict: Record<string, unknown> }
    expect(schema.type).toBe('object')
    expect(Object.keys(schema.dict)).toEqual(FIELDS)
  })

  it('makes every field volatile, or the card would never be offered one', () => {
    for (const [field, meta] of Object.entries(fieldMeta())) {
      expect(meta.volatile, field).toBe(true)
    }
  })

  it('defaults every field instead of requiring it', () => {
    for (const [field, meta] of Object.entries(fieldMeta())) {
      expect(meta.required, field).toBeUndefined()
      expect(meta.default, field).toBeDefined()
    }
  })

  it('accepts a write that carries only the field being edited', () => {
    // One layer per save is what the settings provider produces; a required
    // field is what turned that into a refused save.
    expect(() => Config({ tenantName: 'renamed' } as never)).not.toThrow()
    expect(() => Config({} as never)).not.toThrow()
  })

  it('still gives a deployment that strips a field its documented default', () => {
    expect(resolveSettings({})).toEqual({
      apiUrl: DEFAULT_API_URL,
      tenantName: DEFAULT_TENANT_NAME,
      injectContext: true,
      retainOnStop: true,
    })
  })
})