import type { DotColor } from './colors.ts'
import './ui.css'

interface ProgressBarProps {
  /** 0–1. */
  value: number
  color: DotColor
  /** Track height in px; spec allows 8–10. */
  height?: number
  /** Seconds to wait before growing in, for staggered lists (0.1s apart). */
  delay?: number
  /** Names the bar for screen readers. Leave out when a visible label already says the value. */
  label?: string
}

/** A rounded bar on a well2 track that grows in from zero (spec §5). */
export function ProgressBar({ value, color, height = 8, delay = 0, label }: ProgressBarProps) {
  const pct = Math.round(Math.min(Math.max(value, 0), 1) * 100)
  return (
    <div
      className="progress"
      style={{ height }}
      role={label ? 'meter' : undefined}
      aria-label={label}
      aria-valuenow={label ? pct : undefined}
      aria-valuemin={label ? 0 : undefined}
      aria-valuemax={label ? 100 : undefined}
      aria-hidden={label ? undefined : true}
    >
      <div
        className={`progress-fill fill-${color}`}
        style={{ width: `${pct}%`, animationDelay: `${delay}s` }}
      />
    </div>
  )
}
