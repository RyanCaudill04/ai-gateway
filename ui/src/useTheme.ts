import { useEffect, useState } from 'react'

export type Theme = 'light' | 'dark'

const STORAGE_KEY = 'theme'

function systemTheme(): Theme {
  return window.matchMedia('(prefers-color-scheme: dark)').matches
    ? 'dark'
    : 'light'
}

/** The theme the user picked last time, if they ever pressed the button. */
function savedTheme(): Theme | null {
  try {
    const value = localStorage.getItem(STORAGE_KEY)
    return value === 'light' || value === 'dark' ? value : null
  } catch {
    return null
  }
}

/**
 * Light or dark. Follows the system until the user presses the theme button;
 * after that their pick is remembered. Sets data-theme on <html>, which is
 * what tokens.css keys off.
 */
export function useTheme(): [Theme, () => void] {
  const [picked, setPicked] = useState<Theme | null>(savedTheme)
  const [system, setSystem] = useState<Theme>(systemTheme)
  const theme = picked ?? system

  // Keep up with the system setting while the user hasn't picked one.
  useEffect(() => {
    const query = window.matchMedia('(prefers-color-scheme: dark)')
    const update = () => setSystem(query.matches ? 'dark' : 'light')
    query.addEventListener('change', update)
    return () => query.removeEventListener('change', update)
  }, [])

  useEffect(() => {
    if (picked === null) {
      delete document.documentElement.dataset.theme
    } else {
      document.documentElement.dataset.theme = picked
    }
  }, [picked])

  function toggle() {
    const next: Theme = theme === 'dark' ? 'light' : 'dark'
    setPicked(next)
    try {
      localStorage.setItem(STORAGE_KEY, next)
    } catch {
      // Private mode or blocked storage: the pick still holds until reload.
    }
  }

  return [theme, toggle]
}
