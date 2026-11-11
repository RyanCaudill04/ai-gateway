import { Fragment } from 'react'
import type { Message } from '../types.ts'
import { Icon } from './Icon.tsx'

/**
 * Renders `backticked` spans as <code>. A stand-in until replies get real
 * Markdown rendering.
 */
function InlineText({ text }: { text: string }) {
  return (
    <>
      {text
        .split('`')
        .map((part, i) =>
          i % 2 === 1 ? <code key={i}>{part}</code> : <Fragment key={i}>{part}</Fragment>,
        )}
    </>
  )
}

/** The line under a reply: which provider served it, and any that failed first. */
function ServedBy({ message }: { message: Extract<Message, { role: 'assistant' }> }) {
  const { provider, model, seconds } = message.servedBy
  return (
    <p className="served-by">
      {message.failed.map((hop) => (
        <Fragment key={hop.provider}>
          <span className="level-text-out">
            {hop.provider} {hop.status}
          </span>
          <Icon name="arrowRight" />
        </Fragment>
      ))}
      {provider} · {model} · {seconds.toFixed(1)}s
    </p>
  )
}

export function Thread({ messages }: { messages: Message[] }) {
  if (messages.length === 0) {
    return (
      <div className="thread thread-empty">
        <p>Ask anything to start this chat.</p>
      </div>
    )
  }

  return (
    <div className="thread">
      <div className="thread-inner">
        {messages.map((m) =>
          m.role === 'user' ? (
            <div key={m.id} className="message message-user">
              <InlineText text={m.text} />
            </div>
          ) : (
            <div key={m.id} className="message message-assistant">
              <InlineText text={m.text} />
              <ServedBy message={m} />
            </div>
          ),
        )}
      </div>
    </div>
  )
}
