// Placeholder data for the UI mockup. Nothing here comes from the router yet.
//
// Each export below is marked TODO(api) with where the real data should come
// from. The router endpoints named don't exist yet; the issue numbers are the
// ones that track them. When they do, App should fetch these instead of
// importing them, and this file can go.

import type {
  Command,
  Conversation,
  Message,
  ProviderUsage,
  Route,
} from './types.ts'

// TODO(api): load the virtual models and their fallback order from the router
// (GET /v1/models, #12; names decided in #20).
// Mirrors `routes` in config/providers.example.yaml for now.
export const ROUTES: Route[] = [
  {
    name: 'fast',
    models: [
      'groq/llama-3.3-70b-versatile',
      'gemini/gemini-2.5-flash',
      'ollama/llama3.2:3b',
    ],
  },
  {
    name: 'long-context',
    models: ['gemini/gemini-2.5-flash', 'groq/llama-3.3-70b-versatile'],
  },
  { name: 'private', models: ['ollama/llama3.2:3b'] },
]

// TODO(api): no endpoint is planned for these yet. Decide whether the command
// list is fixed in the UI or reported by the router.
export const COMMANDS: Command[] = [
  {
    name: 'compact',
    description: 'Replace older messages with a summary to free up context',
  },
  { name: 'summarize', description: 'Summarize this chat so far' },
  { name: 'retry', description: 'Ask again on a different provider' },
  { name: 'system', description: 'Set the system prompt for this chat' },
  { name: 'export', description: 'Download this chat as Markdown' },
  { name: 'clear', description: 'Remove every message from this chat' },
]

// TODO(api): load per-provider, per-model usage from the router
// (GET /v1/usage, #17), and refresh it after each reply (#28).
export const PROVIDERS: ProviderUsage[] = [
  {
    kind: 'quota',
    name: 'groq',
    models: [
      { id: 'llama-3.3-70b-versatile', remainingPct: 0, resetsInMin: 41 },
      { id: 'llama-3.1-8b-instant', remainingPct: 68 },
      { id: 'gemma2-9b-it', remainingPct: 91 },
    ],
  },
  {
    kind: 'status',
    name: 'gemini',
    available: true,
    local: false,
    models: ['gemini-2.5-flash'],
  },
  {
    kind: 'quota',
    name: 'openrouter',
    models: [
      { id: 'deepseek-r1:free', remainingPct: 28 },
      { id: 'llama-3.3-70b:free', remainingPct: 55 },
    ],
  },
  {
    kind: 'status',
    name: 'ollama',
    available: true,
    local: true,
    models: ['llama3.2:3b'],
  },
]

// TODO(api): not a router call. Conversations and their messages are to be
// kept in browser storage (#24).
export const CONVERSATIONS: Conversation[] = [
  { id: 'caddy', title: 'Caddy path routing', group: 'Today' },
  { id: 'sqlite', title: 'Go SQLite drivers', group: 'Today' },
  { id: 'tf', title: 'ROS2 TF tree bug', group: 'Yesterday' },
  { id: 'fan', title: 'Pi 5 fan curve', group: 'Yesterday' },
]

export const MESSAGES: Record<string, Message[]> = {
  caddy: [
    {
      role: 'user',
      id: 'm1',
      text: 'How do I route /v1/* to one upstream in Caddy?',
    },
    {
      role: 'assistant',
      id: 'm2',
      text: 'Use a `handle` block with a path matcher, then `reverse_proxy` to the router. Everything else falls through to the UI.',
      servedBy: {
        provider: 'groq',
        model: 'llama-3.3-70b-versatile',
        seconds: 0.4,
      },
      failed: [],
    },
    { role: 'user', id: 'm3', text: 'And keep the prefix?' },
    {
      role: 'assistant',
      id: 'm4',
      text: 'Yes. `handle` keeps it; `handle_path` is the one that strips it.',
      servedBy: { provider: 'gemini', model: 'gemini-2.5-flash', seconds: 1.1 },
      failed: [{ provider: 'groq', status: 429 }],
    },
  ],
}
