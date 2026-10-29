import { describe, expect, it } from 'vitest'
import { Config, resolveSettings } from '../src/index.ts'
import { DEFAULT_API_URL, DEFAULT_BANK_ID } from '../src/settings.ts'

/**
 * The loader schema the Host's settings provider reads.
 *
 * The provider validates the WHOLE composed namespace with this schema on every
 * save, and each volatile edit reaches it as a layer holding only the field
 * being written — the sibling fields are merged in from the layer below. A
 * schema `default()` is folded in only when a key is absent from the value, so
 * a defaulted field left the merged document unset and the whole write was
 * refused ("the server rejected the value"). These cases pin the shape that
 * made that impossible: required fields, no defaults, still volatile.
 */

/** One field's schemastery meta, as the provider's schema object exposes it. */
interface FieldMeta {
  required?: boolean
  volatile?: boolean
  default?: unknown
}

/** The fields the card edits, in the schema's own order. */
const FIELDS = ['apiUrl', 'bankId', 'injectContext', 'retainOnStop']

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

  it('makes every field required instead of defaulted', () => {
    for (const [field, meta] of Object.entries(fieldMeta())) {
      expect(meta.required, field).toBe(true)
      // A default is what made a partial layer validate as a whole section.
      expect(meta.default, field).toBeUndefined()
    }
  })

  it('still gives a deployment that strips a field its documented default', () => {
    // The runtime, not the schema, supplies the fallback — so a missing field
    // is a documented behaviour rather than a refused save.
    expect(resolveSettings({})).toEqual({
      apiUrl: DEFAULT_API_URL,
      bankId: DEFAULT_BANK_ID,
      injectContext: true,
      retainOnStop: true,
    })
  })
})