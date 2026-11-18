import { FlowLine } from '../../components/ui/FlowLine.tsx'
import { ProgressBar } from '../../components/ui/ProgressBar.tsx'
import { TierDot } from '../../components/ui/Dot.tsx'
import { TIER_COLOR } from '../../components/ui/colors.ts'
import {
  TIER_MODELS,
  TIER_ORDER,
  TIERS,
  type RoutedRequest,
  type Tier,
} from '../../sample.ts'

interface RoutingStageProps {
  request: RoutedRequest
  /** Changes on every pick, so the parts that swap remount and rise in again. */
  swapKey: string
}

/**
 * The path one request took: prompt → classifier → three tier lanes, with
 * the chosen lane lit up (spec §8, Routing).
 */
export function RoutingStage({ request, swapKey }: RoutingStageProps) {
  return (
    <section className="routing-stage" aria-label="Routing path">
      <div className="routing-head">
        <div key={`prompt-${swapKey}`} className="routing-prompt routing-swap">
          <span className="routing-prompt-label">Prompt</span>
          <span className="routing-prompt-text">{request.prompt}</span>
        </div>

        {/* Packet and classifier wrap as one, so the line never dangles alone. */}
        <div className="routing-to-classifier">
          {/* Re-keyed too, so a replay sends the packet off from the prompt again. */}
          <div key={`packet-${swapKey}`} className="routing-packet-track" aria-hidden="true">
            <FlowLine direction="horizontal" length={72} live color="muted" />
            <span className="routing-packet" />
          </div>

          <div className="routing-classifier">
            <span className="routing-mark" aria-hidden="true" />
            <span className="routing-classifier-text">
              <span className="routing-classifier-name">Classifier</span>
              <span key={`cms-${swapKey}`} className="routing-classifier-ms mono routing-swap">
                {request.classifierMs} ms to decide
              </span>
            </span>
          </div>
        </div>
      </div>

      {/* Lanes keep their keys across picks so opacity, shift, ring and bar
          width transition smoothly from the old choice to the new one. */}
      <ol className="routing-lanes" aria-label="Tiers">
        {TIER_ORDER.map((tier) => (
          <TierLane
            key={tier}
            tier={tier}
            score={request.scores[tier]}
            chosen={tier === request.tier}
          />
        ))}
      </ol>
    </section>
  )
}

interface TierLaneProps {
  tier: Tier
  /** Classifier confidence, 0–1. */
  score: number
  chosen: boolean
}

function TierLane({ tier, score, chosen }: TierLaneProps) {
  const color = TIER_COLOR[tier]
  return (
    <li className={`routing-lane lane-${color}${chosen ? ' chosen' : ''}`}>
      <FlowLine direction="horizontal" length={64} live={chosen} color={chosen ? color : 'faint'} />
      <span className="routing-tier">
        <span className="routing-tier-name">
          <TierDot tier={tier} size={9} />
          {TIERS[tier].label}
          {chosen && <span className="visually-hidden"> (chosen)</span>}
        </span>
        <span className="mono routing-tier-score">
          <span className="visually-hidden">score </span>
          {score.toFixed(2)}
        </span>
      </span>
      <span className="routing-lane-detail">
        <ProgressBar value={score} color={chosen ? color : 'muted'} />
        <span className="mono routing-chain">
          <span className="visually-hidden">Fallback chain: </span>
          {TIER_MODELS[tier].join(' → ')}
        </span>
      </span>
    </li>
  )
}
