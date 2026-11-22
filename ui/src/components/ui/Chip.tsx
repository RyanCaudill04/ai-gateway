import type { ReactNode } from 'react'
import './ui.css'

interface ChipProps {
  children: ReactNode
  /** Mono for endpoints, versions, signals; sans for status like "live". */
  mono?: boolean
}

/** A small non-interactive pill on a well background. */
export function Chip({ children, mono = false }: ChipProps) {
  return <span className={mono ? 'chip mono' : 'chip'}>{children}</span>
}
