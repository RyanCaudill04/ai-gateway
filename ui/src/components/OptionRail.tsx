import { type ReactNode, useEffect, useRef, useState } from 'react'
import { modelLogo, providerLogo } from '../logos.ts'
import type { Command, ModelChoice, Route } from '../types.ts'
import { Icon, type IconName } from './Icon.tsx'
import { Logo } from './Logo.tsx'

interface MenuItem {
  id: string
  label: string
  /** Shown before the label. */
  icon?: ReactNode
  /** A second, quieter line under the label. */
  detail?: string
  disabled?: boolean
}

interface RailMenuProps {
  /** What the button shows: the current choice's logo or icon. */
  icon: ReactNode
  /** What this button is for, e.g. "Model". */
  title: string
  /** The current choice, shown in the tooltip. Omit for menus with no choice. */
  current?: string
  items: MenuItem[]
  /** The chosen item's id. Omit for menus that run something instead. */
  selectedId?: string
  open: boolean
  onToggle: () => void
  onPick: (id: string) => void
}

/** One icon button and the menu that opens to its left. */
function RailMenu(props: RailMenuProps) {
  const label = props.current ? `${props.title}: ${props.current}` : props.title
  // Menus where one item is the current choice are announced as radio groups.
  const isChoice = props.selectedId !== undefined

  return (
    <div className="rail-item">
      <button
        type="button"
        className="rail-button"
        title={label}
        aria-label={label}
        aria-haspopup="menu"
        aria-expanded={props.open}
        onClick={props.onToggle}
      >
        {props.icon}
      </button>

      {props.open && (
        <div className="rail-menu" role="menu" aria-label={props.title}>
          <p className="rail-menu-title">{props.title}</p>
          {props.items.map((item) => {
            const selected = item.id === props.selectedId
            return (
              <button
                key={item.id}
                type="button"
                role={isChoice ? 'menuitemradio' : 'menuitem'}
                aria-checked={isChoice ? selected : undefined}
                disabled={item.disabled}
                onClick={() => props.onPick(item.id)}
              >
                {item.icon}
                <span className="rail-menu-text">
                  <span className="rail-menu-label">{item.label}</span>
                  {item.detail && (
                    <span className="rail-menu-detail">{item.detail}</span>
                  )}
                </span>
                {selected && (
                  <span className="rail-menu-check">
                    <Icon name="check" />
                  </span>
                )}
              </button>
            )
          })}
        </div>
      )}
    </div>
  )
}

interface OptionRailProps {
  /** Provider names the user can pick from. */
  providers: string[]
  /** The provider picked by hand, or null for auto. */
  provider: string | null
  /** The provider auto would use right now. Undefined if the route has none left. */
  autoProvider: string | undefined
  /** The models on offer: the provider's, or the route's when on auto. */
  models: ModelChoice[]
  /** The model the next message goes to. Undefined if none can take it. */
  model: ModelChoice | undefined
  routes: Route[]
  route: Route
  commands: Command[]
  /** Call with a provider name, or null for auto. */
  onProviderChange: (name: string | null) => void
  /** Call with a "provider/model" ref when the user picks a model by hand. */
  onModelChange: (ref: string) => void
  onRouteChange: (name: string) => void
  /** Call with a command's name, without the slash. */
  onCommand: (name: string) => void
}

type MenuName = 'provider' | 'model' | 'route' | 'commands'

// The provider menu's id for "auto". Real provider names are never empty.
const AUTO = ''

// Routes are named in the config, so only the ones known here get their own
// icon; any other name gets the generic one.
const ROUTE_ICONS: Record<string, IconName> = {
  fast: 'bolt',
  'long-context': 'book',
  private: 'lock',
}

function routeIcon(name: string): ReactNode {
  return <Icon name={ROUTE_ICONS[name] ?? 'sliders'} />
}

/**
 * The chat's options as a column of icon buttons at the top right: provider,
 * model, route (fast, long-context, private), then commands. Each button
 * shows its current choice (the provider's logo, the model's logo, the
 * route's icon) and opens a menu to its left, one at a time.
 */
export function OptionRail(props: OptionRailProps) {
  const [open, setOpen] = useState<MenuName | null>(null)
  const root = useRef<HTMLDivElement>(null)

  // While a menu is open, a click anywhere else or Escape closes it.
  useEffect(() => {
    if (open === null) return
    function onPointerDown(e: PointerEvent) {
      if (!root.current?.contains(e.target as Node)) setOpen(null)
    }
    function onKeyDown(e: KeyboardEvent) {
      if (e.key === 'Escape') setOpen(null)
    }
    document.addEventListener('pointerdown', onPointerDown)
    document.addEventListener('keydown', onKeyDown)
    return () => {
      document.removeEventListener('pointerdown', onPointerDown)
      document.removeEventListener('keydown', onKeyDown)
    }
  }, [open])

  const toggle = (name: MenuName) => () => setOpen(open === name ? null : name)

  // Wraps a handler so that picking an item also closes the menu.
  function closing<T>(handler: (value: T) => void) {
    return (value: T) => {
      setOpen(null)
      handler(value)
    }
  }

  const autoLabel = `auto (${props.autoProvider ?? 'none left'})`
  // On auto, the button shows the provider the gateway would use right now.
  const currentProvider = props.provider ?? props.autoProvider

  return (
    <div
      className="option-rail"
      ref={root}
      role="toolbar"
      aria-label="Chat options"
      aria-orientation="vertical"
    >
      <RailMenu
        icon={<Logo logo={providerLogo(currentProvider)} fallback="server" />}
        title="Provider"
        current={props.provider ?? autoLabel}
        items={[
          {
            id: AUTO,
            label: autoLabel,
            icon: (
              <Logo logo={providerLogo(props.autoProvider)} fallback="server" />
            ),
          },
          ...props.providers.map((name) => ({
            id: name,
            label: name,
            icon: <Logo logo={providerLogo(name)} fallback="server" />,
          })),
        ]}
        selectedId={props.provider ?? AUTO}
        open={open === 'provider'}
        onToggle={toggle('provider')}
        onPick={closing((id) => props.onProviderChange(id === AUTO ? null : id))}
      />
      <RailMenu
        icon={<Logo logo={modelLogo(props.model?.model)} fallback="cube" />}
        title="Model"
        current={props.model?.model ?? 'none available'}
        items={props.models.map((m) => ({
          id: m.ref,
          label: m.model,
          icon: <Logo logo={modelLogo(m.model)} fallback="cube" />,
          detail: m.available ? undefined : 'Out of quota',
          disabled: !m.available,
        }))}
        selectedId={props.model?.ref ?? ''}
        open={open === 'model'}
        onToggle={toggle('model')}
        onPick={closing(props.onModelChange)}
      />
      <RailMenu
        icon={routeIcon(props.route.name)}
        title="Speed and service"
        current={props.route.name}
        items={props.routes.map((r) => ({
          id: r.name,
          label: r.name,
          icon: routeIcon(r.name),
        }))}
        selectedId={props.route.name}
        open={open === 'route'}
        onToggle={toggle('route')}
        onPick={closing(props.onRouteChange)}
      />
      <RailMenu
        icon={<Icon name="slash" />}
        title="Commands"
        items={props.commands.map((c) => ({
          id: c.name,
          label: `/${c.name}`,
          detail: c.description,
        }))}
        open={open === 'commands'}
        onToggle={toggle('commands')}
        onPick={closing(props.onCommand)}
      />
    </div>
  )
}
