import type { Tier } from '../../sample.ts'

/** The three functional colours (plus muted for "not chosen"). */
export type DotColor = 'green' | 'blue' | 'red' | 'muted'

/** Spec §2: Fast is green, Balanced blue, Deep red. */
export const TIER_COLOR: Record<Tier, DotColor> = {
  fast: 'green',
  balanced: 'blue',
  deep: 'red',
}
