import type { ReplyBlock } from './sample.ts'

/**
 * Turns the reply text so far into blocks: prose, and dark code blocks for
 * anything between ``` fences. Safe to call on half a reply (an unclosed fence
 * is just a code block that hasn't ended yet).
 */
export function toBlocks(text: string): ReplyBlock[] {
  return text.split('```').flatMap((part, i): ReplyBlock[] => {
    if (i % 2 === 1) {
      // Drop the language tag after the opening fence.
      return [{ kind: 'code', text: part.replace(/^[^\n]*\n/, '').replace(/\n$/, '') }]
    }
    const prose = part.trim()
    return prose ? [{ kind: 'text', text: prose }] : []
  })
}
