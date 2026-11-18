import type { ReactNode } from 'react'
import './ui.css'

export interface Segment<T extends string> {
  value: T
  label: string
  /** Shown before the label, e.g. a tier dot. */
  icon?: ReactNode
}

interface SegmentedControlProps<T extends string> {
  /** Names the group for screen readers, e.g. "Model tier". */
  label: string
  options: Segment<T>[]
  value: T
  onChange: (value: T) => void
  size?: 'md' | 'sm'
}

/**
 * A pill track of options with one picked (spec §5): the tier picker, the
 * chart range. Announced as a radio group.
 */
export function SegmentedControl<T extends string>({
  label,
  options,
  value,
  onChange,
  size = 'md',
}: SegmentedControlProps<T>) {
  return (
    <div className={`segmented segmented-${size}`} role="radiogroup" aria-label={label}>
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          role="radio"
          aria-checked={o.value === value}
          className="segment"
          onClick={() => onChange(o.value)}
        >
          {o.icon}
          {o.label}
        </button>
      ))}
    </div>
  )
}
