import type { Level, ModelChoice, ProviderUsage, Route } from './types.ts'

/** Bucket a remaining-quota percentage into a colour level. */
export function levelFor(remainingPct: number): Level {
  if (remainingPct <= 5) return 'out'
  if (remainingPct <= 30) return 'low'
  return 'ok'
}

/** The word shown next to a status-only provider's dot. */
export function statusLabel(
  provider: Extract<ProviderUsage, { kind: 'status' }>,
): string {
  if (!provider.available) return 'unavailable'
  return provider.local ? 'local' : 'available'
}

/**
 * One level for a whole provider. On a phone each chip shrinks to a name and
 * a single dot, and this decides that dot's colour.
 *
 * TODO(Ryan): yours to write. Right now every provider reports 'ok', so the
 * phone dots are always green.
 *
 * Things to work out:
 *  - `provider` is a union. What does TypeScript let you read before you check
 *    `provider.kind`, and what does it let you read after?
 *  - For a 'status' provider there's only `available` to go on.
 *  - For a 'quota' provider you have several models. groq in the mock data has
 *    one model at 0% and two that are fine. Should that dot be red (worst
 *    model), green (best model), or amber (mixed)? Pick the rule that tells
 *    you what you'd want to know at a glance. `levelFor` above gives you a
 *    level per model.
 */
export function providerLevel(_provider: ProviderUsage): Level {
  return 'ok'
}

/** Whether a model looks able to take a request, going by the usage data. */
function canServe(provider: ProviderUsage | undefined, model: string): boolean {
  if (provider === undefined) return false
  if (provider.kind === 'status') return provider.available
  const usage = provider.models.find((m) => m.id === model)
  // No usage reported for this model: assume it's fine and let the router try.
  return usage === undefined || usage.remainingPct > 0
}

/**
 * A route's models in fallback order. The first available one is where the
 * next message will most likely go when the provider is left on auto. That's
 * the UI's guess from the last usage data it saw; the router makes the real
 * choice when the request arrives.
 */
export function routeModels(
  route: Route,
  providers: ProviderUsage[],
): ModelChoice[] {
  return route.models.map((ref) => {
    const slash = ref.indexOf('/')
    const provider = ref.slice(0, slash)
    const model = ref.slice(slash + 1)
    const usage = providers.find((p) => p.name === provider)
    return { ref, provider, model, available: canServe(usage, model) }
  })
}

/** Every model one provider offers, for when the user picks that provider. */
export function providerModels(provider: ProviderUsage): ModelChoice[] {
  const ids =
    provider.kind === 'quota'
      ? provider.models.map((m) => m.id)
      : provider.models
  return ids.map((model) => ({
    ref: `${provider.name}/${model}`,
    provider: provider.name,
    model,
    available: canServe(provider, model),
  }))
}
