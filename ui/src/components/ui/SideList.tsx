import type { ReactNode } from 'react'
import './ui.css'

/** The title row at the top of a side list, with an optional chip or button. */
export function SideListHeader({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div className="side-header">
      <h2>{title}</h2>
      {children}
    </div>
  )
}

/** A small muted label above a group of rows, e.g. "Today". */
export function SideListGroup({ label }: { label: string }) {
  return <p className="side-group">{label}</p>
}

interface SideRowProps {
  title: ReactNode
  /** The mono line under the title, e.g. a tier dot + "balanced · 3 turns". */
  meta?: ReactNode
  selected?: boolean
  /** Rows that open a page are links (marked aria-current when selected)... */
  href?: string
  /** ...rows that pick something on this screen are buttons (aria-pressed). */
  onClick?: () => void
}

/** One row in a side list: chats, recent requests or API keys (spec §4). */
export function SideRow({ title, meta, selected = false, href, onClick }: SideRowProps) {
  const className = selected ? 'side-row selected' : 'side-row'
  const content = (
    <>
      <span className="side-row-title">{title}</span>
      {meta !== undefined && <span className="side-row-meta">{meta}</span>}
    </>
  )

  if (href !== undefined) {
    return (
      <a className={className} href={href} aria-current={selected ? 'page' : undefined} onClick={onClick}>
        {content}
      </a>
    )
  }
  return (
    <button type="button" className={className} aria-pressed={selected} onClick={onClick}>
      {content}
    </button>
  )
}
