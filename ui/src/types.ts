// Shapes shared by the mock data and the components. When the router grows a
// real usage endpoint, its JSON should match these.

/** How much is left, bucketed for colour: plenty, running low, or exhausted. */
export type Level = 'ok' | 'low' | 'out'

export interface QuotaModel {
  id: string
  /** 0–100. */
  remainingPct: number
  /** Minutes until the quota refills, when the provider reports it. */
  resetsInMin?: number
}

/**
 * Providers don't all report the same thing. Groq and OpenRouter can say how
 * much quota is left per model; Gemini and Ollama can only say whether they're
 * reachable. `kind` names which shape this is, so TypeScript won't let a
 * component read `models[0].remainingPct` on a provider that has no numbers.
 */
export type ProviderUsage =
  | { kind: 'quota'; name: string; models: QuotaModel[] }
  | {
      kind: 'status'
      name: string
      available: boolean
      /** Runs on this machine (Ollama), so there is no quota to run out. */
      local: boolean
      models: string[]
    }

/** A provider that was tried first and failed, e.g. groq returning 429. */
export interface FailedHop {
  provider: string
  status: number
}

export type Message =
  | { role: 'user'; id: string; text: string }
  | {
      role: 'assistant'
      id: string
      text: string
      servedBy: { provider: string; model: string; seconds: number }
      failed: FailedHop[]
    }

export interface Conversation {
  id: string
  title: string
  group: 'Today' | 'Yesterday'
}

/**
 * A virtual model from providers.yaml: a name the user picks, and the real
 * models tried in order behind it. Each entry is "provider/model".
 */
export interface Route {
  name: string
  models: string[]
}

/** A model the user could send the next message to. */
export interface ModelChoice {
  /** "provider/model". */
  ref: string
  provider: string
  model: string
  /** Whether it can take a request right now, going by the usage data. */
  available: boolean
}

/** A slash command the chat harness understands, e.g. /compact. */
export interface Command {
  name: string
  description: string
}
