import type { LogoDef } from '../logos.ts'
import { Icon, type IconName } from './Icon.tsx'

interface LogoProps {
  logo: LogoDef | undefined
  /** The generic icon to show when there's no logo for this provider or model. */
  fallback: IconName
}

/** A brand logo. Decorative: the button or menu item around it carries the name. */
export function Logo({ logo, fallback }: LogoProps) {
  if (logo === undefined) return <Icon name={fallback} />

  if (logo.mono) {
    // The SVG is used as a stencil over a block of the text colour, because an
    // <img> can't pick up the page's colours.
    const mask = `url("${logo.src}")`
    return (
      <span
        className="logo logo-mono"
        style={{ maskImage: mask, WebkitMaskImage: mask }}
        aria-hidden="true"
      />
    )
  }
  return <img className="logo" src={logo.src} alt="" />
}
