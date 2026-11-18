import { TierDot } from '../../components/ui/Dot.tsx'
import type { Reply, Turn } from './sample.ts'

/** The mono line above a reply: `● tier · model · provider · latency` (spec §5). */
function ReplyMeta({ reply }: { reply: Reply }) {
  return (
    <span className="chat-meta">
      <TierDot tier={reply.tier} size={8} />
      {reply.tier} · {reply.model} · {reply.provider} · {reply.ms} ms
    </span>
  )
}

/**
 * Four pulsing squares in the thinking colours, then the tier and model
 * (spec §5). A status role so screen readers hear "Thinking" once.
 */
function ThinkingIndicator({ reply }: { reply: Reply }) {
  return (
    <div className="chat-thinking rise" role="status">
      <span className="chat-thinking-squares" aria-hidden="true">
        <span className="fill-blue" />
        <span className="fill-red" />
        <span className="fill-green" />
        <span className="fill-blue" />
      </span>
      <span className="chat-thinking-label">Thinking</span>
      <span className="chat-thinking-model">
        {reply.tier} · {reply.model}
      </span>
    </div>
  )
}

interface ChatTurnProps {
  turn: Turn
  /** While true the reply is still on its way, so show the indicator instead. */
  thinking: boolean
}

/** A user prompt and the model's reply to it. */
export function ChatTurn({ turn, thinking }: ChatTurnProps) {
  const { reply } = turn
  return (
    <article className="chat-turn">
      <p className="chat-prompt rise">{turn.prompt}</p>
      {thinking ? (
        <ThinkingIndicator reply={reply} />
      ) : (
        <div className="chat-reply rise">
          <ReplyMeta reply={reply} />
          {reply.blocks.map((block, i) =>
            block.kind === 'code' ? (
              <pre key={i} className="chat-code">
                <code>{block.text}</code>
              </pre>
            ) : (
              <p key={i} className="chat-text">
                {block.text}
              </p>
            ),
          )}
        </div>
      )}
    </article>
  )
}
