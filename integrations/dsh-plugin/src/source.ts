/**
 * Message sources this plugin declares in the harness's merge-extensible
 * `MessageSourceMap`.
 *
 * `dsh >= 0.2.0` removed the shared catch-all `plugin` kind: each producer
 * names itself, so a context this plugin attaches is attributable in the log
 * and a future reader can tell recalled memory from something the user said.
 * @module @studyzy/dsh-wenyuan-memory
 */

/** The block-carrying context this plugin injects. */
export interface WenYuanMemorySource {
  /** Producer kind, spelled from this package's own name. */
  readonly kind: 'wenyuan-memory'
  /** What kind of thing it is: material lifted out of the bank's memory. */
  readonly form: 'recall'
}

declare module '@deepseek-ai/dsh-llm' {
  interface MessageSourceMap {
    /** Recalled WenYuan memory attached as context, never as a user turn. */
    'wenyuan-memory': WenYuanMemorySource
  }
}