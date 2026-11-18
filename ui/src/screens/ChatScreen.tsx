import { useEffect, useRef, useState } from 'react'
import { Composer } from '../components/Composer.tsx'
import { Icon } from '../components/Icon.tsx'
import { ScreenLayout } from '../components/shell/ScreenLayout.tsx'
import { Chip } from '../components/ui/Chip.tsx'
import { Dot, TierDot } from '../components/ui/Dot.tsx'
import { PillButton } from '../components/ui/PillButton.tsx'
import { SideListGroup, SideListHeader, SideRow } from '../components/ui/SideList.tsx'
import { CHATS } from '../sample.ts'
import { screenHref } from '../screens.ts'
import { ChatTurn } from './chat/ChatTurn.tsx'
import { TurnInspector } from './chat/TurnInspector.tsx'
import { CANNED_REPLY, estimateTokens, THINK_MS, THREAD, type Turn } from './chat/sample.ts'
import './ChatScreen.css'

/**
 * Which turn is still "thinking", if any. Clears itself after THINK_MS, and
 * starts over whenever a new turn is handed in. TODO(api): end when the
 * router's stream actually finishes instead of on a timer.
 */
function useThinking(initial: string | null) {
  const [thinkingId, setThinkingId] = useState(initial)

  useEffect(() => {
    if (thinkingId === null) return
    const timer = window.setTimeout(() => setThinkingId(null), THINK_MS)
    return () => window.clearTimeout(timer)
  }, [thinkingId])

  return [thinkingId, setThinkingId] as const
}

export function ChatScreen() {
  // TODO(api): every chat shows the same sample thread for now.
  const [chatId, setChatId] = useState(CHATS[0].id)
  const [turns, setTurns] = useState<Turn[]>(THREAD)
  // Like the mockup, open with the last reply still thinking so the state is visible.
  const [thinkingId, setThinkingId] = useThinking(THREAD[THREAD.length - 1].id)
  const streamRef = useRef<HTMLElement>(null)

  const chat = CHATS.find((c) => c.id === chatId) ?? CHATS[0]
  const latest = turns[turns.length - 1]

  // Keep the newest message in view as turns arrive and replies replace the indicator.
  useEffect(() => {
    const stream = streamRef.current
    if (stream) stream.scrollTop = stream.scrollHeight
  }, [turns, thinkingId])

  function send(text: string) {
    const turn: Turn = {
      id: crypto.randomUUID(),
      prompt: text,
      tokens: estimateTokens(text),
      reply: CANNED_REPLY,
    }
    setTurns((prev) => [...prev, turn])
    setThinkingId(turn.id)
  }

  const side = (
    <>
      <SideListHeader title="Chats">
        <a className="chat-new" href={screenHref('new')} aria-label="New chat">
          <Icon name="plus" />
        </a>
      </SideListHeader>
      <SideListGroup label="Today" />
      {CHATS.map((c) => (
        <SideRow
          key={c.id}
          title={c.title}
          meta={
            <>
              <TierDot tier={c.tier} />
              {c.tier} · {c.turns} turns
            </>
          }
          selected={c.id === chatId}
          onClick={() => setChatId(c.id)}
        />
      ))}
    </>
  )

  const main = (
    <>
      {/* CardHeader's styles, but with the chip beside the title rather than in the h1. */}
      <header className="card-header chat-header">
        <div className="chat-title">
          <h1>{chat.title}</h1>
          <Chip>
            <Dot color="blue" />
            Auto · mostly Balanced
          </Chip>
        </div>
        {/* TODO: sharing a thread needs a backend; the button does nothing yet. */}
        <PillButton>Share</PillButton>
      </header>

      <section className="chat-stream" aria-label="Conversation" ref={streamRef}>
        {turns.map((turn) => (
          <ChatTurn key={turn.id} turn={turn} thinking={turn.id === thinkingId} />
        ))}
      </section>

      <section className="chat-dock" aria-label="Reply">
        <Composer variant="dock" placeholder="Follow up…" label="Reply" onSend={send} />
      </section>
    </>
  )

  return (
    <ScreenLayout
      sideLabel="Conversations"
      side={side}
      main={main}
      inspectorLabel="This turn"
      inspector={<TurnInspector turn={latest} thinking={thinkingId === latest.id} />}
    />
  )
}
