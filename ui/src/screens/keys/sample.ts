// Sample data for the Keys & usage screen, copied from
// design/source/Keys.dc.html. TODO(api): the router has no key management or
// per-key usage yet; replace each export once it does.

import type { Tier } from '../../sample.ts'

export interface ApiKey {
  id: string
  name: string
  /** When the key last made a request, already worded ("2 min ago"). */
  lastUsed: string
  /** The key with all but its prefix and last four characters hidden. */
  masked: string
  /** Which tiers the key may use, as shown next to the key. */
  scope: string
  /** The same thing, shortened for the inspector. */
  tiersAllowed: string
  errorsToday: number
}

export const KEYS: ApiKey[] = [
  {
    id: 'portfolio-site',
    name: 'portfolio-site',
    lastUsed: '2 min ago',
    masked: 'cur_live_••••••••••••••••3f9a',
    scope: 'tier: auto',
    tiersAllowed: 'All',
    errorsToday: 2,
  },
  {
    id: 'pi-watchdog',
    name: 'pi-watchdog',
    lastUsed: '1 h ago',
    masked: 'cur_live_••••••••••••••••b71c',
    scope: 'tier: fast only',
    tiersAllowed: 'Fast',
    errorsToday: 0,
  },
  {
    id: 'edge-platform',
    name: 'edge-platform',
    lastUsed: 'yesterday',
    masked: 'cur_live_••••••••••••••••09de',
    scope: 'tier: auto',
    tiersAllowed: 'All',
    errorsToday: 5,
  },
]

/** What "Show" reveals. A placeholder: the real secret is only shown once, at creation. */
export const REVEALED_KEY = 'cur_live_[YOUR KEY]'

export type Range = '24h' | '7d' | '30d'

export const RANGES: Range[] = ['24h', '7d', '30d']

/** Requests over each chart range. The mockup shows the same numbers for every key. */
export const SERIES: Record<Range, { total: string; start: string; label: string; points: number[] }> = {
  '24h': {
    total: '1,204',
    start: '24h ago',
    label: 'the last 24 hours',
    points: [12, 18, 9, 6, 4, 5, 11, 26, 44, 52, 61, 58, 49, 63, 70, 66, 59, 72, 81, 64, 48, 37, 29, 22],
  },
  '7d': {
    total: '8,931',
    start: '7 days ago',
    label: 'the last 7 days',
    points: [40, 52, 47, 61, 58, 74, 69, 83, 77, 92, 88, 71, 66, 95],
  },
  '30d': {
    total: '31,560',
    start: '30 days ago',
    label: 'the last 30 days',
    points: [
      22, 30, 28, 35, 41, 38, 46, 44, 52, 49, 57, 61, 55, 63, 70, 66, 72, 69, 78, 74, 81, 86, 79,
      88, 92, 85, 94, 90, 97, 99,
    ],
  },
}

export interface KeyRequest {
  time: string
  tier: Tier
  model: string
  /** HTTP status from the provider. Anything but 200 means the router fell back. */
  status: number
  ms: number
}

export const RECENT: KeyRequest[] = [
  { time: '14:02:11', tier: 'balanced', model: 'llama-3.3-70b', status: 200, ms: 412 },
  { time: '14:01:47', tier: 'fast', model: 'gemini-2.5-flash-lite', status: 200, ms: 186 },
  { time: '13:58:30', tier: 'deep', model: 'gemini-2.5-pro', status: 200, ms: 2140 },
  { time: '13:55:02', tier: 'fast', model: 'llama-3.1-8b', status: 429, ms: 94 },
  { time: '13:52:19', tier: 'balanced', model: 'qwen-3-32b', status: 200, ms: 978 },
  { time: '13:49:40', tier: 'deep', model: 'deepseek-r1', status: 200, ms: 3310 },
]

/** Free-tier requests used today against each provider's daily limit. */
export const HEADROOM: { name: string; used: number; limit: number }[] = [
  { name: 'Groq', used: 640, limit: 1000 },
  { name: 'Google AI Studio', used: 310, limit: 1500 },
  { name: 'Cerebras', used: 120, limit: 1000 },
  { name: 'OpenRouter', used: 46, limit: 50 },
]
