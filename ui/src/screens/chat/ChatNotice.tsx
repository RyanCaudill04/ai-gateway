import type { ReactNode } from 'react'
import { Dot } from '../../components/ui/Dot.tsx'

interface ChatNoticeProps {
  /** blue = working, green = ready, red = something failed (spec §2 colours). */
  tone: 'busy' | 'ready' | 'error'
  title: string
  children?: ReactNode
}

const DOT = { busy: 'blue', ready: 'green', error: 'red' } as const

/**
 * A status pill for the states the mockup doesn't draw: models loading, ready
 * to chat, or something failed. A well with a status dot, like the
 * inspector's Status card; errors announce themselves, the rest are polite.
 */
export function ChatNotice({ tone, title, children }: ChatNoticeProps) {
  return (
    <div className={`chat-notice chat-notice-${tone}`} role={tone === 'error' ? 'alert' : 'status'}>
      <Dot color={DOT[tone]} size={9} pulse={tone === 'busy'} />
      <span className="chat-notice-title">{title}</span>
      {children && <span className="chat-notice-body">{children}</span>}
    </div>
  )
}
