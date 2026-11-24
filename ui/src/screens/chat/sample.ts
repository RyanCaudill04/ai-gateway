// The sample conversation the Chat screen shows, copied from
// design/source/Chat.dc.html. TODO(api): load the thread from the router and
// stream real replies instead of the canned one below.

import type { Tier } from '../../sample.ts'

/** A piece of a reply: a paragraph, or a dark code block. */
export type ReplyBlock = { kind: 'text'; text: string } | { kind: 'code'; text: string }

/** One model reply and how the router got it. */
export interface Reply {
  tier: Tier
  model: string
  provider: string
  /** Total latency. */
  ms: number
  /** How long the classifier took to decide. */
  classifierMs: number
  /** Classifier confidence per tier, 0–1. */
  scores: Record<Tier, number>
  blocks: ReplyBlock[]
}

/** A user prompt and the reply to it. */
export interface Turn {
  id: string
  prompt: string
  /** Prompt size the classifier saw. */
  tokens: number
  reply: Reply
  /** The reply's raw text, kept so the conversation can be sent back as history. */
  raw?: string
  /** True while the reply is still arriving. */
  streaming?: boolean
  /** Why the reply stopped early, shown under whatever text arrived. */
  error?: string
}

/** The API key the screen pretends these requests came in on. */
export const KEY_NAME = 'portfolio-site'

/** How long the fake "thinking" lasts before the reply shows (from the mockup). */
export const THINK_MS = 4500

const SCORES: Record<Tier, number> = { fast: 0.09, balanced: 0.62, deep: 0.29 }

export const THREAD: Turn[] = [
  {
    id: 't1',
    prompt: 'How should I debounce duplicate requests hitting the router within the same second?',
    tokens: 14,
    reply: {
      tier: 'balanced',
      model: 'llama-3.3-70b',
      provider: 'Groq',
      ms: 412,
      classifierMs: 10,
      scores: SCORES,
      blocks: [
        {
          kind: 'text',
          text: 'Key each request by a hash of its tier and normalized prompt, and keep in-flight requests in a map. A duplicate that arrives while the first is running awaits the same promise instead of calling the provider again.',
        },
        {
          kind: 'code',
          text: `const inflight = new Map();

export function dedupe(key, run) {
  if (inflight.has(key)) return inflight.get(key);
  const p = run().finally(() => inflight.delete(key));
  inflight.set(key, p);
  return p;
}`,
        },
      ],
    },
  },
  {
    id: 't2',
    prompt: 'Would this still work across multiple Workers instances?',
    tokens: 14,
    reply: {
      tier: 'balanced',
      model: 'llama-3.3-70b',
      provider: 'Groq',
      ms: 978,
      classifierMs: 10,
      scores: SCORES,
      blocks: [
        {
          kind: 'text',
          text: "Not as written: the map lives inside one isolate, so two instances never see each other's requests. Move the in-flight key into a Durable Object named by the hash. Every duplicate then lands in the same object and shares one upstream call.",
        },
      ],
    },
  },
]

/** What every follow-up gets until the screen talks to the router. */
export const CANNED_REPLY: Reply = {
  tier: 'balanced',
  model: 'llama-3.3-70b',
  provider: 'Groq',
  ms: 640,
  classifierMs: 9,
  scores: { fast: 0.14, balanced: 0.66, deep: 0.2 },
  blocks: [
    {
      kind: 'text',
      text: "This is a sample reply. The chat screen isn't connected to the router yet, so every follow-up gets this same answer after a short think.",
    },
  ],
}

/** A rough token count (about four characters each) until the router reports the real one. */
export function estimateTokens(text: string): number {
  return Math.max(1, Math.round(text.length / 4))
}
