// Which brand logo goes with which provider or model. See assets/logos/README.md
// for where the files come from.

import deepseek from './assets/logos/deepseek.svg'
import gemini from './assets/logos/gemini.svg'
import gemma from './assets/logos/gemma.svg'
import groq from './assets/logos/groq.svg'
import meta from './assets/logos/meta.svg'
import ollama from './assets/logos/ollama.svg'
import openrouter from './assets/logos/openrouter.svg'

export interface LogoDef {
  src: string
  /**
   * A single-colour logo with no colour of its own. It's drawn in the page's
   * text colour so it shows up on both the light and dark themes.
   */
  mono: boolean
}

const PROVIDER_LOGOS: Record<string, LogoDef> = {
  groq: { src: groq, mono: true },
  gemini: { src: gemini, mono: false },
  openrouter: { src: openrouter, mono: true },
  ollama: { src: ollama, mono: true },
}

// Models are matched on how their id starts, since one family has many ids
// (llama-3.3-70b-versatile, llama3.2:3b, ...). Llama is Meta's model.
const MODEL_LOGOS: [prefix: string, logo: LogoDef][] = [
  ['llama', { src: meta, mono: false }],
  ['gemini', { src: gemini, mono: false }],
  ['gemma', { src: gemma, mono: false }],
  ['deepseek', { src: deepseek, mono: false }],
]

/** The provider's logo, or undefined if there isn't one for it yet. */
export function providerLogo(name: string | undefined): LogoDef | undefined {
  return name === undefined ? undefined : PROVIDER_LOGOS[name]
}

/** The logo of the model's maker, or undefined if the model isn't recognised. */
export function modelLogo(id: string | undefined): LogoDef | undefined {
  if (id === undefined) return undefined
  const lower = id.toLowerCase()
  return MODEL_LOGOS.find(([prefix]) => lower.startsWith(prefix))?.[1]
}
