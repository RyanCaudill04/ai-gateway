import type { Conversation } from '../types.ts'
import { HEALTH_LABEL, type RouterHealth } from '../useRouterHealth.ts'
import { Icon } from './Icon.tsx'

const GROUPS: Conversation['group'][] = ['Today', 'Yesterday']

interface SidebarProps {
  conversations: Conversation[]
  activeId: string
  health: RouterHealth
  onSelect: (id: string) => void
  onNewChat: () => void
}

export function Sidebar({
  conversations,
  activeId,
  health,
  onSelect,
  onNewChat,
}: SidebarProps) {
  return (
    <nav className="sidebar" aria-label="Conversations">
      <div className="brand">
        <img src="/favicon.svg" alt="" width="28" height="28" />
        <span>AI Gateway</span>
      </div>

      <button type="button" className="sidebar-item new-chat" onClick={onNewChat}>
        <Icon name="plus" />
        New chat
      </button>

      {GROUPS.map((group) => {
        const items = conversations.filter((c) => c.group === group)
        if (items.length === 0) return null
        return (
          <div key={group} className="sidebar-group">
            <h2>{group}</h2>
            {items.map((c) => (
              <button
                key={c.id}
                type="button"
                className="sidebar-item"
                aria-current={c.id === activeId ? 'page' : undefined}
                onClick={() => onSelect(c.id)}
              >
                {c.title}
              </button>
            ))}
          </div>
        )
      })}

      <p className="health" role="status">
        <span
          className={`dot level-${health === 'ok' ? 'ok' : health === 'down' ? 'out' : 'none'}`}
        />
        {HEALTH_LABEL[health]}
      </p>
    </nav>
  )
}
