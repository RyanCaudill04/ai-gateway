import { useState } from 'react'
import './App.css'
import { Composer } from './components/Composer.tsx'
import { Icon } from './components/Icon.tsx'
import { OptionRail } from './components/OptionRail.tsx'
import { Sidebar } from './components/Sidebar.tsx'
import { Thread } from './components/Thread.tsx'
import { UsageStrip } from './components/UsageStrip.tsx'
import {
  COMMANDS,
  CONVERSATIONS,
  MESSAGES,
  PROVIDERS,
  ROUTES,
} from './mock.ts'
import type { Conversation, Message } from './types.ts'
import { providerModels, routeModels } from './usage.ts'
import { useRouterHealth } from './useRouterHealth.ts'

function App() {
  const health = useRouterHealth()
  const [conversations, setConversations] = useState(CONVERSATIONS)
  const [messages, setMessages] = useState(MESSAGES)
  const [activeId, setActiveId] = useState(CONVERSATIONS[0].id)
  const [routeName, setRouteName] = useState(ROUTES[0].name)
  // The provider the user picked by hand. Null means auto: the route decides.
  const [providerName, setProviderName] = useState<string | null>(null)
  // A model the user picked by hand, as "provider/model". Null means the
  // first available one.
  const [pinnedRef, setPinnedRef] = useState<string | null>(null)
  // Phones only: whether the conversation list has slid in over the chat.
  const [drawerOpen, setDrawerOpen] = useState(false)

  const active = conversations.find((c) => c.id === activeId)
  const route = ROUTES.find((r) => r.name === routeName) ?? ROUTES[0]
  const provider = PROVIDERS.find((p) => p.name === providerName)

  // What auto would pick: the first model in the route that still has quota.
  const autoModel = routeModels(route, PROVIDERS).find((m) => m.available)
  // The chosen provider decides which models are on offer. On auto, they're
  // the route's models.
  const models = provider
    ? providerModels(provider)
    : routeModels(route, PROVIDERS)
  // Where the next message goes: the user's pick if they made one, otherwise
  // the first model on offer that can take a request.
  const model =
    models.find((m) => m.ref === pinnedRef) ?? models.find((m) => m.available)

  function changeProvider(name: string | null) {
    setProviderName(name)
    setPinnedRef(null)
  }

  function changeRoute(name: string) {
    setRouteName(name)
    if (providerName === null) setPinnedRef(null)
  }

  // TODO(Ryan): runs when a command is picked from the options at the top
  // right. It should put `/compact ` (or whichever was picked) in the message
  // box. See the TODO in components/Composer.tsx.
  function pickCommand(_name: string) {}

  function selectConversation(id: string) {
    setActiveId(id)
    setDrawerOpen(false)
  }

  function startNewChat() {
    const chat: Conversation = {
      id: crypto.randomUUID(),
      title: 'New chat',
      group: 'Today',
    }
    setConversations([chat, ...conversations])
    selectConversation(chat.id)
  }

  // Mockup only: adds the user's message. Nothing is sent to the router and
  // no reply comes back yet.
  // TODO(api): send the conversation to POST /v1/chat/completions with the
  // chosen route or pinned model, and stream the reply into the thread (#9,
  // #23). The provider and model that answered come back with it (#26).
  function sendMessage(text: string) {
    const message: Message = { role: 'user', id: crypto.randomUUID(), text }
    setMessages({
      ...messages,
      [activeId]: [...(messages[activeId] ?? []), message],
    })
  }

  return (
    <div className={drawerOpen ? 'app drawer-open' : 'app'}>
      <Sidebar
        conversations={conversations}
        activeId={activeId}
        health={health}
        onSelect={selectConversation}
        onNewChat={startNewChat}
      />
      <div
        className="backdrop"
        aria-hidden="true"
        onClick={() => setDrawerOpen(false)}
      />

      <main className="main">
        <header className="topbar narrow-only">
          <button
            type="button"
            className="icon-button"
            aria-label="Open conversations"
            aria-expanded={drawerOpen}
            onClick={() => setDrawerOpen(true)}
          >
            <Icon name="menu" />
          </button>
          <h1>{active?.title}</h1>
          <button
            type="button"
            className="icon-button"
            aria-label="New chat"
            onClick={startNewChat}
          >
            <Icon name="plus" />
          </button>
        </header>

        <UsageStrip providers={PROVIDERS} />
        <div className="thread-area">
          <Thread messages={messages[activeId] ?? []} />
          <OptionRail
            providers={PROVIDERS.map((p) => p.name)}
            provider={providerName}
            autoProvider={autoModel?.provider}
            models={models}
            model={model}
            routes={ROUTES}
            route={route}
            commands={COMMANDS}
            onProviderChange={changeProvider}
            onModelChange={setPinnedRef}
            onRouteChange={changeRoute}
            onCommand={pickCommand}
          />
        </div>
        <div className="composer-area">
          <Composer onSend={sendMessage} />
          {/* Which provider and model the next message will go to. */}
          <p className="next-model">
            {model
              ? `${model.provider} · ${model.model}`
              : 'No model available'}
          </p>
        </div>
      </main>
    </div>
  )
}

export default App
