import { Dot } from '../../components/ui/Dot.tsx'
import { FlowLine } from '../../components/ui/FlowLine.tsx'
import { ProgressBar } from '../../components/ui/ProgressBar.tsx'
import { TIER_COLOR } from '../../components/ui/colors.ts'
import { TIER_MODELS, TIER_ORDER, TIERS } from '../../sample.ts'
import { KEY_NAME, type Turn } from './sample.ts'

interface TurnInspectorProps {
  /** The latest turn: the one being answered, or the last one answered. */
  turn: Turn
  thinking: boolean
}

/** The right-hand card on Chat: how the router handled this turn (spec §8). */
export function TurnInspector({ turn, thinking }: TurnInspectorProps) {
  const { reply } = turn
  // The second model in the tier's chain is what the router would try next.
  const fallback = TIER_MODELS[reply.tier][1] ?? '—'

  return (
    <>
      <section className="well chat-status" aria-label="Status">
        <span className="chat-status-text">
          <Dot color={thinking ? 'blue' : turn.error ? 'red' : 'green'} size={9} pulse={thinking} />
          {thinking ? 'Thinking' : turn.error ? 'Failed' : 'Answered'}
        </span>
        <span className="chat-mono-sub">
          {thinking ? `streaming from ${reply.provider}` : `${reply.ms} ms`}
        </span>
      </section>

      <section className="well chat-routing" aria-label="Routing path">
        <h2 className="well-label">Routing</h2>
        <div className="chat-step">
          Prompt<span className="chat-step-value">{turn.tokens} tokens</span>
        </div>
        {/* The lines only flow while the request is in flight (spec §6). */}
        <span className="chat-flow">
          <FlowLine direction="vertical" length={14} live={thinking} color="muted" />
        </span>
        <div className="chat-step chat-step-classifier">
          Classifier<span className="chat-step-value">{reply.classifierMs} ms</span>
        </div>
        <span className="chat-flow">
          <FlowLine
            direction="vertical"
            length={14}
            live={thinking}
            color={TIER_COLOR[reply.tier]}
          />
        </span>
        <ul className="chat-tiers">
          {TIER_ORDER.map((tier) => (
            <li
              key={tier}
              className={`chat-tier chat-tier-${TIER_COLOR[tier]}${tier === reply.tier ? ' chosen' : ''}`}
              aria-current={tier === reply.tier ? 'true' : undefined}
            >
              <Dot color={TIER_COLOR[tier]} size={8} />
              {TIERS[tier].label}
            </li>
          ))}
        </ul>
      </section>

      <section className="well chat-scores" aria-label="Tier scores">
        <h2 className="well-label">Classifier confidence</h2>
        {TIER_ORDER.map((tier, i) => (
          <div key={tier} className="chat-score">
            <div className="chat-score-row">
              <span>{TIERS[tier].label}</span>
              <span className="chat-mono-sub">{reply.scores[tier].toFixed(2)}</span>
            </div>
            <ProgressBar
              value={reply.scores[tier]}
              color={tier === reply.tier ? TIER_COLOR[tier] : 'muted'}
              delay={i * 0.1}
            />
          </div>
        ))}
      </section>

      <section className="well chat-served" aria-label="Served by">
        <dl>
          <div>
            <dt>Model</dt>
            <dd>{reply.model}</dd>
          </div>
          <div>
            <dt>Provider</dt>
            <dd>{reply.provider}</dd>
          </div>
          <div>
            <dt>Fallback</dt>
            <dd>{fallback}</dd>
          </div>
          <div>
            <dt>Key</dt>
            <dd className="mono chat-served-key">{KEY_NAME}</dd>
          </div>
        </dl>
      </section>
    </>
  )
}
