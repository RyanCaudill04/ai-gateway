import type { ButtonHTMLAttributes } from 'react'
import './ui.css'

interface PillButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  /** "danger" is red text on a well, for Revoke. Never a red fill (spec §2). */
  tone?: 'default' | 'danger' | 'primary'
}

/** A rounded button: Share, Show, Revoke key, Create key. */
export function PillButton({ tone = 'default', className, ...props }: PillButtonProps) {
  return (
    <button
      type="button"
      {...props}
      className={`pill-button pill-${tone}${className ? ` ${className}` : ''}`}
    />
  )
}
