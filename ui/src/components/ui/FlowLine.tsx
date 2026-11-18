import type { DotColor } from './colors.ts'
import './ui.css'

interface FlowLineProps {
  direction: 'horizontal' | 'vertical'
  /** Length in px. */
  length: number
  /** Live lines animate their dashes; only while work happens or on the chosen lane (spec §6). */
  live?: boolean
  color?: DotColor | 'faint'
}

/** A dashed connector that shows work flowing between two steps. Decorative. */
export function FlowLine({ direction, length, live = false, color = 'blue' }: FlowLineProps) {
  const size = direction === 'horizontal' ? { width: length } : { height: length }
  return (
    <span
      className={`flow flow-${direction} text-${color}${live ? ' live' : ''}`}
      style={size}
      aria-hidden="true"
    />
  )
}
