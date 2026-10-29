/**
 * The merged-memory tag vocabulary: the block markers the Host writes into
 * model-visible context, and the strip that keeps them out of a write-back.
 *
 * The Stop-time write-back is a conversation, not a memory dump. When the same
 * memory is attached to the next turn as context and the turn is then retained
 * verbatim, the bank slowly accumulates its own output and every later recall
 * matches it. The reference's Stop hook answers this by stripping its injected
 * blocks before assembling the transcript; this module is the same rule, with
 * the tag names this plugin actually emits.
 *
 * Import-free on purpose: the Host half strips, the browser half never needs
 * it, but keeping it free of runtime imports means a test can include it in
 * either program.
 * @module @studyzy/dsh-wenyuan-memory/context-blocks
 */

/** The context block attached at session start and before a prompt. */
export const RECALL_BLOCK_TAG = 'wenyuan_memory'

/** The block attached when the bank's knowledge pages could be read. */
export const KNOWLEDGE_BLOCK_TAG = 'wenyuan_knowledge'

/**
 * Every block this plugin injects, plus the reference-side spellings a
 * deployment may already carry in a prompt, so a transcript assembled from
 * either harness is stripped the same way.
 */
export const INJECTED_BLOCK_TAGS: readonly string[] = [
  RECALL_BLOCK_TAG,
  KNOWLEDGE_BLOCK_TAG,
  'hindsight_memory',
  'hindsight_memory_refresh',
  'hindsight_knowledge',
  'hindsight_knowledge_refresh',
]

/** Matches one whole injected block, opening tag through its closing tag. */
const INJECTED_BLOCK_RE = new RegExp(
  `<(${INJECTED_BLOCK_TAGS.join('|')})\\b[\\s\\S]*?</\\1>`,
  'g',
)

/**
 * Remove every injected memory block from one text.
 * @param text - the text as it appears in the transcript.
 * @returns the text with the blocks removed; everything else is untouched.
 */
export function stripInjectedBlocks(text: string): string {
  return text.replace(INJECTED_BLOCK_RE, '')
}

/**
 * Wrap recalled memory in the block a later strip recognizes.
 * @param tag - the block tag to use.
 * @param body - the block's content.
 * @returns the tag-wrapped block.
 */
export function contextBlock(tag: string, body: string): string {
  return `<${tag}>\n${body.trim()}\n</${tag}>`
}