import { useEffect, useRef, useState } from 'react'
import { listModels, streamChat, type ChatMessage, type ModelInfo } from '../api/chat.ts'
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
import { ChatNotice } from './chat/ChatNotice.tsx'
import { toBlocks } from './chat/reply.ts'
import { CANNED_REPLY, estimateTokens, type Turn } from './chat/sample.ts'
import './ChatScreen.css'

/** The model to chat with: the first real (non-route) one, preferring hosted over local. */
function pickModel(models: ModelInfo[]) {
  const real = models.filter((m) => !m.virtual)
  return real.find((m) => !m.local) ?? real[0] ?? null
}

/** The conversation so far, in the shape the router's API takes. */
function historyOf(turns: Turn[]): ChatMessage[] {
  return turns.flatMap((t) => [
    { role: 'user' as const, content: t.prompt },
    { role: 'assistant' as const, content: t.raw ?? '' },
  ])
}

export function ChatScreen() {
  // TODO(api): the side list is still sample data; only the open conversation is live.
  const [chatId, setChatId] = useState(CHATS[0].id)
  const [turns, setTurns] = useState<Turn[]>([])
  const [thinkingId, setThinkingId] = useState<string | null>(null)
  const [model, setModel] = useState<ModelInfo | null>(null)
  const [error, setError] = useState<string | null>(null)
  const streamRef = useRef<HTMLElement>(null)
  const abortRef = useRef<AbortController | null>(null)

  const chat = CHATS.find((c) => c.id === chatId) ?? CHATS[0]
  const latest = turns[turns.length - 1]
  const busy = turns.some((t) => t.streaming)

  // Load the model to chat with, and stop any reply still streaming on the way out.
  useEffect(() => {
    const controller = new AbortController()
    listModels(controller.signal)
      .then((models) => {
        const picked = pickModel(models)
        setModel(picked)
        if (!picked) setError('No model is available. Add a provider key and reload.')
      })
      .catch((e: unknown) => {
        if (!controller.signal.aborted) setError(e instanceof Error ? e.message : String(e))
      })
    return () => {
      controller.abort()
      abortRef.current?.abort()
    }
  }, [])

  // Keep the newest message in view as the reply grows.
  useEffect(() => {
    const stream = streamRef.current
    if (stream) stream.scrollTop = stream.scrollHeight
  }, [turns, thinkingId])

  /** Replace one turn by id. */
  function patch(id: string, change: (t: Turn) => Turn) {
    setTurns((prev) => prev.map((t) => (t.id === id ? change(t) : t)))
  }

  async function send(text: string) {
    if (!model) return // the composer is disabled until a model has loaded
    setError(null)

    const id = crypto.randomUUID()
    const history = historyOf(turns)
    const turn: Turn = {
      id,
      prompt: text,
      tokens: estimateTokens(text),
      raw: '',
      streaming: true,
      // TODO(api): tier, classifier numbers and the fallback come from the
      // classifier, which doesn't exist yet. Only model, provider and ms are real.
      reply: { ...CANNED_REPLY, model: model.id.slice(model.provider.length + 1), provider: model.provider, ms: 0, blocks: [] },
    }
    setTurns((prev) => [...prev, turn])
    setThinkingId(id)

    const controller = new AbortController()
    abortRef.current = controller
    const started = performance.now()
    let raw = ''
    try {
      await streamChat(
        model.id,
        [...history, { role: 'user', content: text }],
        (token) => {
          raw += token
          setThinkingId(null) // first token: show the reply instead of the indicator
          patch(id, (t) => ({ ...t, raw, reply: { ...t.reply, blocks: toBlocks(raw) } }))
        },
        controller.signal,
      )
    } catch (e) {
      if (!controller.signal.aborted) {
        const message = e instanceof Error ? e.message : String(e)
        patch(id, (t) => ({ ...t, error: message }))
      }
    } finally {
      const ms = Math.round(performance.now() - started)
      patch(id, (t) => ({ ...t, streaming: false, reply: { ...t.reply, ms } }))
      setThinkingId((current) => (current === id ? null : current))
      if (abortRef.current === controller) abortRef.current = null
    }
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
        {turns.length === 0 &&
          (error ? (
            <ChatNotice tone="error" title="Can't chat yet">
              {error}
            </ChatNotice>
          ) : model ? (
            <ChatNotice tone="ready" title="Ready">
              {model.provider} · {model.id.slice(model.provider.length + 1)}
            </ChatNotice>
          ) : (
            <ChatNotice tone="busy" title="Loading models" />
          ))}
        {turns.map((turn) => (
          <ChatTurn key={turn.id} turn={turn} thinking={turn.id === thinkingId} />
        ))}
      </section>

      <section className="chat-dock" aria-label="Reply">
        {error && turns.length > 0 && (
          <ChatNotice tone="error" title="Couldn't send">
            {error}
          </ChatNotice>
        )}
        <Composer variant="dock" placeholder="Follow up…" label="Reply" onSend={send} disabled={busy || !model} />
      </section>
    </>
  )

  return (
    <ScreenLayout
      sideLabel="Conversations"
      side={side}
      main={main}
      inspectorLabel="This turn"
      inspector={latest ? <TurnInspector turn={latest} thinking={thinkingId === latest.id} /> : null}
    />
  )
}
