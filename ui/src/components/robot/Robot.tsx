import { useId } from 'react'
import { gradientId, robot, type GradientName, type Part, type RobotMode } from './geometry.ts'
import './Robot.css'

export type { RobotMode }

interface RobotProps {
  /** idle, typing (listening) or thinking (spec §7, reactive states). */
  mode?: RobotMode
  /** Where the eyes look, −1 (left) to 1 (right). */
  lx?: number
  /** Where the eyes look, −1 (up) to 1 (down). */
  ly?: number
  /** Screen-reader name for the picture. */
  label?: string
  /** For sizing from the screen, e.g. a max-width. */
  className?: string
}

const clamp = (n: number) => Math.max(-1, Math.min(1, n))

// The five fan blades, 72° apart.
const BLADES = [0, 72, 144, 216, 288]

/**
 * The landing page's voxel robot (spec §7). It drops in part by part, then
 * floats, blinks and waves; `mode` changes its face and speeds things up.
 *
 * Its colours are fixed rather than themed: it's the one decorative source of
 * colour, and the white shell reads on both the light and dark stage.
 */
export function Robot({ mode = 'idle', lx = 0, ly = 0, label = "Current's robot", className }: RobotProps) {
  // useId gives something like «r1»; keep only characters that are safe
  // inside url(#…), so each robot's gradients have their own ids.
  const uid = 'robot' + useId().replace(/[^\w-]/g, '')
  const id = (name: GradientName) => gradientId(uid, name)
  const { partsA, partsB, headStyle } = robot(mode, clamp(lx), clamp(ly), uid)

  return (
    <svg
      className={`robot robot-${mode}${className ? ' ' + className : ''}`}
      viewBox="-85 -150 200 290"
      role="img"
      aria-label={label}
    >
      <defs>
        <linearGradient id={id('sTop')} x1="0" y1="0" x2="1" y2="1">
          <stop offset="0" stopColor="#FFFFFF" />
          <stop offset="1" stopColor="#EDF1F5" />
        </linearGradient>
        <linearGradient id={id('sFront')} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0" stopColor="#F8FAFC" />
          <stop offset="1" stopColor="#D6DDE6" />
        </linearGradient>
        <linearGradient id={id('sSide')} x1="0" y1="0" x2="1" y2="1">
          <stop offset="0" stopColor="#D3DAE3" />
          <stop offset="1" stopColor="#A9B3C1" />
        </linearGradient>
        <linearGradient id={id('dTop')} x1="0" y1="0" x2="1" y2="1">
          <stop offset="0" stopColor="#4B525D" />
          <stop offset="1" stopColor="#343A43" />
        </linearGradient>
        <linearGradient id={id('dFront')} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0" stopColor="#30363F" />
          <stop offset="1" stopColor="#1A1E24" />
        </linearGradient>
        <linearGradient id={id('dSide')} x1="0" y1="0" x2="1" y2="1">
          <stop offset="0" stopColor="#23282F" />
          <stop offset="1" stopColor="#101317" />
        </linearGradient>
        <linearGradient id={id('visor')} x1="0" y1="0" x2="1" y2="1">
          <stop offset="0" stopColor="#202938" />
          <stop offset="1" stopColor="#05070A" />
        </linearGradient>
        <linearGradient id={id('pcb')} x1="0" y1="0" x2="1" y2="1">
          <stop offset="0" stopColor="#24935F" />
          <stop offset="1" stopColor="#13563A" />
        </linearGradient>
        <radialGradient id={id('shadowG')} cx="0.5" cy="0.5" r="0.5">
          <stop offset="0" stopColor="#0C1220" stopOpacity="0.28" />
          <stop offset="1" stopColor="#0C1220" stopOpacity="0" />
        </radialGradient>
      </defs>

      {/* The soft ground shadow: no base under it (spec §7). */}
      <g className="robot-shadow-in">
        <ellipse className="robot-shadow" cx="16" cy="96" rx="78" ry="26" fill={`url(#${id('shadowG')})`} />
      </g>

      <g className="robot-float">
        <Parts parts={partsA} />
        {/* The fan sits on the head's top plane, so it lifts with the head. */}
        <g className="robot-drop" style={{ animationDelay: '1.5s' }}>
          <g className="robot-follow" style={headStyle}>
            <g transform="matrix(11.26 6.5 -11.26 6.5 19.1 -98.1)">
              <circle r="2" fill="#15191F" />
              <circle r="1.7" fill="#2A3038" />
              <g className="robot-fan">
                {BLADES.map((deg) => (
                  <path
                    key={deg}
                    d="M0 0 C0.6 -0.7 1.4 -0.6 1.6 -0.2 L0.2 0.2 Z"
                    fill="#B7C0CC"
                    transform={deg ? `rotate(${deg})` : undefined}
                  />
                ))}
              </g>
              <circle r="0.4" fill="#4285F4" />
            </g>
          </g>
        </g>
        <Parts parts={partsB} />
      </g>
    </svg>
  )
}

function Parts({ parts }: { parts: Part[] }) {
  return parts.map((pt, i) => (
    <g key={i} className={pt.outer} style={pt.outerStyle}>
      <g className={pt.inner || undefined} style={pt.innerStyle}>
        {pt.polys.map((p, j) => (
          <polygon key={j} points={p.pts} fill={p.fill} className={p.cls || undefined} style={p.style} />
        ))}
      </g>
    </g>
  ))
}
