import { useState } from 'react'
import { Icon } from './Icon.tsx'
import './Composer.css'

interface ComposerProps {
  /** Call with the message text. The screen adds it to the conversation. */
  onSend: (text: string) => void
  /**
   * Runs on every keystroke. The landing robot uses it to look like it's
   * listening; it doesn't need the text.
   */
  onInput?: () => void
  /** True while a reply is being written: sending is paused. */
  disabled?: boolean
  /** "hero" is the big two-line box on the landing page; "dock" sits under a chat. */
  variant?: 'hero' | 'dock'
  placeholder?: string
  /** Screen-reader name for the box. */
  label?: string
}

/**
 * The message box and send button (spec §5: a well pill with a round blue
 * Send button and a blue focus ring). Enter sends, Shift+Enter adds a line.
 */
export function Composer({
  onSend,
  onInput,
  disabled = false,
  variant = 'dock',
  placeholder = 'Follow up…',
  label = 'Message',
}: ComposerProps) {
  const [text, setText] = useState('')

  function submit() {
    const message = text.trim()
    if (!message || disabled) return
    onSend(message)
    setText('')
  }

  return (
    <form
      className={`composer composer-${variant}`}
      onSubmit={(e) => {
        e.preventDefault()
        submit()
      }}
    >
      <textarea
        className="composer-input"
        rows={variant === 'hero' ? 2 : 1}
        placeholder={placeholder}
        aria-label={label}
        value={text}
        onChange={(e) => setText(e.target.value)}
        onInput={onInput}
        onKeyDown={(e) => {
          if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
            e.preventDefault()
            submit()
          }
        }}
      />
      <button type="submit" className="send" aria-label="Send message" disabled={disabled}>
        <Icon name="arrowUp" />
      </button>
    </form>
  )
}
