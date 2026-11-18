import { TierDot } from '../../components/ui/Dot.tsx'
import { TIER_COLOR } from '../../components/ui/colors.ts'
import { TIER_ORDER, TIERS } from '../../sample.ts'
import { FALLBACKS_TODAY, TODAY_BY_TIER } from './sample.ts'

interface RoutingInspectorProps {
  /** What the classifier picked up on in the selected request. */
  signals: string[]
  swapKey: string
}

/** The right-hand wells: this request's signals, then today's totals. */
export function RoutingInspector({ signals, swapKey }: RoutingInspectorProps) {
  return (
    <>
      <section className="well routing-well">
        <h2 className="well-label">Signals the classifier saw</h2>
        <ul key={swapKey} className="routing-signals routing-swap">
          {signals.map((s) => (
            <li key={s} className="routing-signal mono">
              {s}
            </li>
          ))}
        </ul>
      </section>

      <section className="well routing-well routing-today">
        <h2 className="well-label">Today by tier</h2>
        {/* The list below says the same numbers in words. */}
        <div className="routing-split" aria-hidden="true">
          {TIER_ORDER.map((tier) => (
            <span
              key={tier}
              className={`fill-${TIER_COLOR[tier]}`}
              style={{ width: `${TODAY_BY_TIER[tier]}%` }}
            />
          ))}
        </div>
        <ul className="routing-split-list">
          {TIER_ORDER.map((tier) => (
            <li key={tier}>
              <TierDot tier={tier} size={8} />
              {TIERS[tier].label}
              <span className="mono">{TODAY_BY_TIER[tier]}%</span>
            </li>
          ))}
        </ul>
      </section>

      <section className="well routing-well routing-fallbacks">
        <h2 className="well-label">Fallbacks today</h2>
        <span className="big-number">{FALLBACKS_TODAY}</span>
        <p>Times a provider was skipped for rate limits or errors.</p>
      </section>
    </>
  )
}
