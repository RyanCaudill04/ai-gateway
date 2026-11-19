import { useEffect, useState } from 'react'
import type { IconName } from './components/Icon.tsx'

/** The four screens in the top bar, in order. */
export const SCREENS = [
  { id: 'new', label: 'New', icon: 'newChat' },
  { id: 'chats', label: 'Chats', icon: 'chats' },
  { id: 'routing', label: 'Routing', icon: 'routing' },
  { id: 'keys', label: 'Keys & usage', icon: 'key' },
] as const satisfies readonly { id: string; label: string; icon: IconName }[]

export type ScreenId = (typeof SCREENS)[number]['id']

export function screenHref(id: ScreenId): string {
  return `#/${id}`
}

function screenFromHash(): ScreenId {
  const id = window.location.hash.replace(/^#\/?/, '')
  return SCREENS.find((s) => s.id === id)?.id ?? 'new'
}

/**
 * Which screen is open, read from the URL hash (#/chats). Links change the
 * hash, so the back button and bookmarks work without a router library.
 */
export function useScreen(): ScreenId {
  const [screen, setScreen] = useState(screenFromHash)

  useEffect(() => {
    const update = () => setScreen(screenFromHash())
    window.addEventListener('hashchange', update)
    return () => window.removeEventListener('hashchange', update)
  }, [])

  return screen
}
