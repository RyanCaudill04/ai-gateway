import type { Tier } from '../../sample.ts'
import { TIER_COLOR, type DotColor } from './colors.ts'
import './ui.css'

interface DotProps {
  color: DotColor
  /** Diameter in px. Spec allows 6–9. */
  size?: number
  /** Pulses while something is in progress (status dots). */
  pulse?: boolean
}

/** A small coloured circle. Decorative: always put a text label next to it. */
export function Dot({ color, size = 7, pulse = false }: DotProps) {
  return (
    <span
      className={`dot dot-${color}${pulse ? ' pulse' : ''}`}
      style={{ width: size, height: size }}
      aria-hidden="true"
    />
  )
}

/** The hollow ring that stands for "Auto" in the tier picker. */
export function RingDot({ size = 8 }: { size?: number }) {
  return (
    <span className="dot dot-ring" style={{ width: size, height: size }} aria-hidden="true" />
  )
}

export function TierDot({ tier, size }: { tier: Tier; size?: number }) {
  return <Dot color={TIER_COLOR[tier]} size={size} />
}
