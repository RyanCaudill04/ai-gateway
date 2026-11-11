import { Icon } from './Icon.tsx'

interface ComposerProps {
  /** Call with the message text. App adds it to the open conversation. */
  onSend: (text: string) => void
}

/**
 * The message box and send button.
 *
 * TODO(Ryan): yours to write. The markup and styling are done, but nothing
 * here sends yet: the textarea isn't tracked and the button has no handler.
 * `props.onSend` already works on the App side, so once you call it the
 * message appears in the thread.
 *
 * Things to work out:
 *  - Where does the text the user has typed live? Look at how the route
 *    works: App holds `routeName`, hands it down to OptionRail, and OptionRail
 *    reports changes back up through `onRouteChange`. The textarea needs the
 *    same `value` + `onChange` pattern.
 *  - Commands are picked in OptionRail, which is a sibling of this component,
 *    and App's `pickCommand` runs when one is chosen. Picking `/compact`
 *    should put `/compact ` in this box. Given that, can the text live in
 *    here, or does it have to live somewhere both components can reach?
 *  - Enter should send and Shift+Enter should add a new line. Which event
 *    tells you a key was pressed, and how do you stop Enter from also
 *    inserting a newline?
 *  - What should happen when the box is empty or only spaces?
 *  - What should the box contain right after sending?
 */
export function Composer(_props: ComposerProps) {
  return (
    <form className="composer" onSubmit={(e) => e.preventDefault()}>
      <textarea
        className="composer-input"
        rows={1}
        placeholder="Ask anything"
        aria-label="Message"
      />
      <button type="submit" className="send" aria-label="Send message">
        <Icon name="arrowUp" />
      </button>
    </form>
  )
}
