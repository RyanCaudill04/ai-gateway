import { SCREENS, screenHref, type ScreenId } from '../../screens.ts'
import type { Theme } from '../../useTheme.ts'
import { Icon } from '../Icon.tsx'
import { BrandMark } from './BrandMark.tsx'
import './TopBar.css'

interface TopBarProps {
  screen: ScreenId
  theme: Theme
  onToggleTheme: () => void
}

/** Logo, the four screens as pill tabs, and the theme button (spec §4). */
export function TopBar({ screen, theme, onToggleTheme }: TopBarProps) {
  return (
    <nav className="card topbar" aria-label="Primary">
      <a className="topbar-brand" href={screenHref('new')}>
        <BrandMark />
        current
      </a>

      <div className="topbar-tabs">
        {SCREENS.map((s) => (
          <a
            key={s.id}
            className="topbar-tab"
            href={screenHref(s.id)}
            aria-current={s.id === screen ? 'page' : undefined}
          >
            <Icon name={s.icon} />
            {s.label}
          </a>
        ))}
      </div>

      <button
        type="button"
        className="topbar-theme"
        aria-label={theme === 'dark' ? 'Switch to light theme' : 'Switch to dark theme'}
        onClick={onToggleTheme}
      >
        <Icon name={theme === 'dark' ? 'sun' : 'moon'} />
      </button>
    </nav>
  )
}
