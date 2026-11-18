import type { ReactNode } from 'react'
import './ScreenLayout.css'

interface ScreenLayoutProps {
  /** Names the side list for screen readers, e.g. "Conversations". */
  sideLabel: string
  /** The 280px list on the left: chats, requests or keys. Hidden on phones. */
  side: ReactNode
  /** The big card in the middle. */
  main: ReactNode
  /** Names the inspector for screen readers, e.g. "Overview". */
  inspectorLabel: string
  /** The narrow card on the right: a stack of wells. */
  inspector: ReactNode
}

/**
 * The three cards under the top bar that every screen shares (spec §4): side
 * list, main card (8 of 12 columns) and inspector (4 of 12).
 */
export function ScreenLayout({
  sideLabel,
  side,
  main,
  inspectorLabel,
  inspector,
}: ScreenLayoutProps) {
  return (
    <div className="screen">
      <aside className="card screen-side" aria-label={sideLabel}>
        {side}
      </aside>
      <div className="screen-body">
        <main className="card screen-main">{main}</main>
        <aside className="card screen-inspector" aria-label={inspectorLabel}>
          {inspector}
        </aside>
      </div>
    </div>
  )
}
