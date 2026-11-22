// Sample data for the Current redesign, copied from the mockups in
// design/source/. Nothing here comes from the router yet.
//
// Every screen reads from this one file so they all show the same fake
// world. TODO(api): replace each export with real router data. The design's
// Fast/Balanced/Deep tiers don't exist in the router yet (it has routes:
// fast, long-context, private); see the open question on #163.

/** A model tier the classifier can pick (spec §2, "Meaning of each colour"). */
export type Tier = 'fast' | 'balanced' | 'deep'

/** What the user can pick in the tier picker: a tier, or let the classifier decide. */
export type TierChoice = 'auto' | Tier

export const TIER_ORDER: Tier[] = ['fast', 'balanced', 'deep']

export const TIERS: Record<Tier, { label: string; hint: string }> = {
  fast: { label: 'Fast', hint: 'Small, quick models for short answers.' },
  balanced: { label: 'Balanced', hint: 'Mid-size models for everyday work.' },
  deep: { label: 'Deep', hint: 'The strongest free model, for hard problems.' },
}

export const AUTO_HINT = 'The classifier picks a tier for you.'

/** Each tier's fallback chain, shown in Routing as "model → fallback". */
export const TIER_MODELS: Record<Tier, string[]> = {
  fast: ['llama-3.1-8b', 'gemini-2.5-flash-lite'],
  balanced: ['llama-3.3-70b', 'qwen-3-32b'],
  deep: ['gemini-2.5-pro', 'deepseek-r1'],
}

export interface ChatSummary {
  id: string
  title: string
  tier: Tier
  turns: number
}

export const CHATS: ChatSummary[] = [
  { id: 'debounce', title: 'Debounce in the router', tier: 'balanced', turns: 3 },
  { id: 'tunnel', title: 'Cloudflare tunnel headers', tier: 'fast', turns: 5 },
  { id: 'classifier', title: 'Classifier feature ideas', tier: 'deep', turns: 9 },
  { id: 'edge', title: 'Edge node registration', tier: 'deep', turns: 14 },
  { id: 'watchdog', title: 'Pi watchdog alerts', tier: 'fast', turns: 2 },
]

export interface ProviderStatus {
  name: string
  up: boolean
  latencyMs: number
}

export const PROVIDERS: ProviderStatus[] = [
  { name: 'Groq', up: true, latencyMs: 210 },
  { name: 'Google AI Studio', up: true, latencyMs: 340 },
  { name: 'Cerebras', up: true, latencyMs: 180 },
  { name: 'OpenRouter', up: true, latencyMs: 520 },
]

/** One request through the classifier, as the Routing screen replays it. */
export interface RoutedRequest {
  id: string
  prompt: string
  tier: Tier
  /** Total latency. */
  ms: number
  /** How long the classifier took to decide. */
  classifierMs: number
  model: string
  provider: string
  /** Classifier confidence per tier, 0–1, in TIER_ORDER. */
  scores: Record<Tier, number>
  signals: string[]
}

export const REQUESTS: RoutedRequest[] = [
  {
    id: 'r1',
    prompt: 'Debounce duplicate requests within one second',
    tier: 'balanced',
    ms: 412,
    classifierMs: 9,
    model: 'llama-3.3-70b',
    provider: 'Groq',
    scores: { fast: 0.18, balanced: 0.71, deep: 0.11 },
    signals: ['code-related', 'design question', '14 tokens'],
  },
  {
    id: 'r2',
    prompt: 'What does HTTP 524 mean?',
    tier: 'fast',
    ms: 186,
    classifierMs: 7,
    model: 'gemini-2.5-flash-lite',
    provider: 'Google AI Studio',
    scores: { fast: 0.86, balanced: 0.12, deep: 0.02 },
    signals: ['factual', 'short', '6 tokens'],
  },
  {
    id: 'r3',
    prompt: 'Design a schema for edge node registration with heartbeats',
    tier: 'deep',
    ms: 2140,
    classifierMs: 11,
    model: 'gemini-2.5-pro',
    provider: 'Google AI Studio',
    scores: { fast: 0.04, balanced: 0.29, deep: 0.67 },
    signals: ['multi-step', 'system design', '11 tokens'],
  },
  {
    id: 'r4',
    prompt: 'Write a commit message for this diff',
    tier: 'fast',
    ms: 231,
    classifierMs: 8,
    model: 'llama-3.1-8b',
    provider: 'Groq',
    scores: { fast: 0.79, balanced: 0.19, deep: 0.02 },
    signals: ['rewrite', 'code attached'],
  },
  {
    id: 'r5',
    prompt: 'Compare Durable Objects vs KV for rate limits',
    tier: 'balanced',
    ms: 978,
    classifierMs: 10,
    model: 'qwen-3-32b',
    provider: 'Cerebras',
    scores: { fast: 0.09, balanced: 0.62, deep: 0.29 },
    signals: ['comparison', 'infrastructure'],
  },
  {
    id: 'r6',
    prompt: 'Prove this scheduling heuristic is optimal',
    tier: 'deep',
    ms: 3310,
    classifierMs: 12,
    model: 'deepseek-r1',
    provider: 'OpenRouter',
    scores: { fast: 0.02, balanced: 0.18, deep: 0.8 },
    signals: ['reasoning', 'math'],
  },
]

/** The public endpoint shown in the landing header. */
export const ENDPOINT = 'ai.ryancaudill.dev/v1'
