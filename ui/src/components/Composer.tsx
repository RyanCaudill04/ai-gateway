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
  /** "hero" is the big two-line box on the landing page; "dock" sits under a chat. */
  variant?: 'hero' | 'dock'
  placeholder?: string
  /** Screen-reader name for the box. */
  label?: string
}

/**
 * The message box and send button (spec §5: a well pill with a round blue
 * Send button and a blue focus ring).
 *
 * TODO(Ryan): yours to write. The markup and styling are done, but nothing
 * here sends yet: the textarea isn't tracked and the button has no handler.
 * `props.onSend` is how the text gets back to the screen, so once you call it
 * the message can appear in the thread (and the landing robot starts
 * thinking).
 *
 * Things to work out:
 *  - Where does the text the user has typed live? Look at how the tier
 *    picker works: the screen holds `tier`, hands it down to TierPicker, and
 *    TierPicker reports changes back up through `onChange`. The textarea
 *    needs the same `value` + `onChange` pattern.
 *  - Clicking a "Start with" prompt on the landing page should put that
 *    prompt in this box. Given that, can the text live in here, or does it
 *    have to live somewhere both can reach?
 *  - Enter should send and Shift+Enter should add a new line. Which event
 *    tells you a key was pressed, and how do you stop Enter from also
 *    inserting a newline?
 *  - What should happen when the box is empty or only spaces?
 *  - What should the box contain right after sending?
 */
export function Composer({
  onInput,
  variant = 'dock',
  placeholder = 'Follow up…',
  label = 'Message',
}: ComposerProps) {
  return (
    <form className={`composer composer-${variant}`} onSubmit={(e) => e.preventDefault()}>
      <textarea
        className="composer-input"
        rows={variant === 'hero' ? 2 : 1}
        placeholder={placeholder}
        aria-label={label}
        onInput={onInput}
      />
      <button type="submit" className="send" aria-label="Send message">
        <Icon name="arrowUp" />
      </button>
    </form>
  )
}
