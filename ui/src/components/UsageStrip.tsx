import { useState } from 'react'
import type { ProviderUsage } from '../types.ts'
import { levelFor, providerLevel, statusLabel } from '../usage.ts'
import { Icon } from './Icon.tsx'

/** A horizontal track filled to `pct`, coloured by how much is left. */
function Bar({ pct, className }: { pct: number; className: string }) {
  return (
    <span className={`${className} level-${levelFor(pct)}`}>
      {/* A sliver stays visible at 0% so an empty bar still reads as a bar. */}
      <span style={{ width: `${Math.max(pct, 4)}%` }} />
    </span>
  )
}

/** What a chip shows after the provider's name. */
function ChipSummary({ provider }: { provider: ProviderUsage }) {
  if (provider.kind === 'status') {
    return (
      <>
        <span className={`dot level-${provider.available ? 'ok' : 'out'}`} />
        <span className="wide-only">{statusLabel(provider)}</span>
      </>
    )
  }
  return (
    <>
      {/* Wide screens: one small bar per model. */}
      <span className="chip-bars wide-only">
        {provider.models.map((m) => (
          <Bar key={m.id} pct={m.remainingPct} className="mini-bar" />
        ))}
      </span>
      {/* Phones: a single dot summarising the whole provider. */}
      <span className={`dot narrow-only level-${providerLevel(provider)}`} />
    </>
  )
}

/** The per-model breakdown for one provider. */
function ProviderDetail({ provider }: { provider: ProviderUsage }) {
  if (provider.kind === 'status') {
    return (
      <ul className="usage-rows">
        {provider.models.map((id) => (
          <li key={id} className="usage-row usage-row-status">
            <span className="usage-model">{id}</span>
            <span className="usage-state">
              <span
                className={`dot level-${provider.available ? 'ok' : 'out'}`}
              />
              {statusLabel(provider)}
            </span>
          </li>
        ))}
      </ul>
    )
  }
  return (
    <ul className="usage-rows">
      {provider.models.map((m) => (
        <li key={m.id} className="usage-row">
          <span className="usage-model">
            {m.id}
            {m.resetsInMin !== undefined && (
              <span className="usage-reset"> · resets in {m.resetsInMin}m</span>
            )}
          </span>
          <Bar pct={m.remainingPct} className="bar" />
          <span className={`usage-pct level-text-${levelFor(m.remainingPct)}`}>
            {Math.round(m.remainingPct)}%
          </span>
        </li>
      ))}
    </ul>
  )
}

/**
 * Usage left per provider, as a strip of chips above the chat.
 *
 * Clicking a chip opens a panel underneath. On wide screens the panel shows
 * only the chip's provider; on phones it fills the screen and lists them all
 * (the stylesheet decides which, so there's no screen-size logic here).
 */
export function UsageStrip({ providers }: { providers: ProviderUsage[] }) {
  const [open, setOpen] = useState<string | null>(null)

  return (
    <div className="usage">
      <div className="usage-strip" role="group" aria-label="Usage left">
        {providers.map((p) => (
          <button
            key={p.name}
            type="button"
            className="chip"
            aria-expanded={open === p.name}
            onClick={() => setOpen(open === p.name ? null : p.name)}
          >
            <span>{p.name}</span>
            <ChipSummary provider={p} />
          </button>
        ))}
      </div>

      {open !== null && (
        <div className="usage-panel">
          <div className="usage-panel-head narrow-only">
            <h2>Usage left</h2>
            <button
              type="button"
              className="icon-button"
              aria-label="Close usage"
              onClick={() => setOpen(null)}
            >
              <Icon name="x" />
            </button>
          </div>
          {providers.map((p) => (
            <section
              key={p.name}
              className={p.name === open ? 'is-selected' : undefined}
            >
              <h3>{p.name}</h3>
              <ProviderDetail provider={p} />
            </section>
          ))}
        </div>
      )}
    </div>
  )
}
