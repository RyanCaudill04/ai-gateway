import { useEffect, useState } from 'react'
import { Icon } from '../../components/Icon.tsx'
import { PillButton } from '../../components/ui/PillButton.tsx'
import { REVEALED_KEY, type ApiKey } from './sample.ts'

/**
 * The key itself, masked until Show is pressed, with its scope and a copy
 * button. KeysScreen remounts this for each key, so picking another key
 * always starts hidden.
 */
export function KeyWell({ apiKey }: { apiKey: ApiKey }) {
  const [shown, setShown] = useState(false)
  const [copied, setCopied] = useState(false)
  const text = shown ? REVEALED_KEY : apiKey.masked

  // Show the tick for a moment, then go back to the copy icon.
  useEffect(() => {
    if (!copied) return
    const timer = setTimeout(() => setCopied(false), 1500)
    return () => clearTimeout(timer)
  }, [copied])

  async function copy() {
    // The clipboard API only exists on secure pages (https or localhost),
    // and the browser can still refuse it, so fail quietly.
    if (!navigator.clipboard) return
    try {
      await navigator.clipboard.writeText(text)
      setCopied(true)
    } catch {
      // Nothing useful to tell the user; the key is still on screen.
    }
  }

  return (
    <section className="well keys-key" aria-label="Key">
      <code className="keys-key-text">{text}</code>
      <span className="keys-key-scope">{apiKey.scope}</span>
      <div className="keys-key-actions">
        <PillButton
          className="keys-key-button keys-show"
          aria-label={`${shown ? 'Hide' : 'Show'} ${apiKey.name} key`}
          onClick={() => setShown(!shown)}
        >
          {shown ? 'Hide' : 'Show'}
        </PillButton>
        <PillButton className="keys-key-button keys-copy" aria-label="Copy key" onClick={copy}>
          <Icon name={copied ? 'check' : 'copy'} />
        </PillButton>
      </div>
      <span className="visually-hidden" role="status">
        {copied ? 'Copied' : ''}
      </span>
    </section>
  )
}
