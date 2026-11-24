// Fake numbers that only the Routing screen shows, copied from
// design/source/Routing.dc.html. The shared requests and fallback chains
// live in ../../sample.ts.

import type { Tier } from '../../sample.ts'

/** TODO(api): the classifier's real version string. The mockup leaves it as a placeholder. */
export const CLASSIFIER_VERSION = '[VERSION]'

/** TODO(api): share of today's requests per tier, in percent. Adds up to 100. */
export const TODAY_BY_TIER: Record<Tier, number> = {
  fast: 52,
  balanced: 34,
  deep: 14,
}

/** TODO(api): how many times a provider was skipped today. A placeholder in the mockup too. */
export const FALLBACKS_TODAY = '[COUNT]'
