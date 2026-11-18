import type { ReactNode } from 'react'
import './ui.css'

/** The title row at the top of a main card, with chips or buttons on the right. */
export function CardHeader({ title, children }: { title: ReactNode; children?: ReactNode }) {
  return (
    <header className="card-header">
      <h1>{title}</h1>
      {children}
    </header>
  )
}
