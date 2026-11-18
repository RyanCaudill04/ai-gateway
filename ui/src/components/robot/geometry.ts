// The robot's shape, built as isometric polygons. Ported from robot() in
// design/source/Main.dc.html; the numbers and the part order are the
// design's, so change them there first (spec §7).
//
// Pure: no React state, no DOM. Robot.tsx turns the result into SVG.

import type { CSSProperties } from 'react'

export type RobotMode = 'idle' | 'typing' | 'thinking'

/** One face of a voxel: four projected points and how to paint them. */
export interface Poly {
  pts: string
  fill: string
  cls: string
  style?: CSSProperties
}

/**
 * A group of polygons that drops in together. The outer <g> runs the intro
 * drop; the inner <g> moves with the head, follows the cursor or waves.
 */
export interface Part {
  outer: string
  outerStyle?: CSSProperties
  inner: string
  innerStyle?: CSSProperties
  polys: Poly[]
}

export interface RobotGeometry {
  /** Everything drawn behind the fan: left arm, legs, body, neck, head, face. */
  partsA: Part[]
  /** Everything drawn in front of it: antenna, right arm. */
  partsB: Part[]
  /** The head's lift, shared with the fan so it rides along. */
  headStyle: CSSProperties
}

/** The gradients the robot paints with. Robot.tsx defines one of each. */
export const GRADIENTS = ['sTop', 'sFront', 'sSide', 'dTop', 'dFront', 'dSide', 'visor', 'pcb', 'shadowG'] as const
export type GradientName = (typeof GRADIENTS)[number]

/** An element id that's unique per robot, so two on a page don't share gradients. */
export function gradientId(uid: string, name: GradientName): string {
  return `${uid}-${name}`
}

type Faces = [top: string, front: string, side: string]

/**
 * Builds the robot for a mode, with the eyes looking toward (lx, ly), each
 * from −1 to 1. `uid` prefixes the gradient ids (see gradientId).
 */
export function robot(mode: RobotMode, lx: number, ly: number, uid: string): RobotGeometry {
  const url = (name: GradientName) => `url(#${gradientId(uid, name)})`

  // Isometric projection of a unit cube at 13px (spec §7).
  const s = 13
  const P = (x: number, y: number, z: number) =>
    ((x - y) * 0.866 * s).toFixed(1) + ',' + ((x + y) * 0.5 * s - z * s).toFixed(1)
  const poly = (a: string[], fill: string, cls = '', style?: CSSProperties): Poly => ({
    pts: a.join(' '),
    fill,
    cls,
    style,
  })
  // A box drawn as its three visible faces: top, front, right side.
  const cube = (x: number, y: number, z: number, w: number, d: number, h: number, c: Faces, cls?: string) => [
    poly([P(x, y, z + h), P(x + w, y, z + h), P(x + w, y + d, z + h), P(x, y + d, z + h)], c[0], cls),
    poly([P(x, y + d, z), P(x + w, y + d, z), P(x + w, y + d, z + h), P(x, y + d, z + h)], c[1], cls),
    poly([P(x + w, y, z), P(x + w, y + d, z), P(x + w, y + d, z + h), P(x + w, y, z + h)], c[2], cls),
  ]
  // A flat panel on a front face (fixed y) or a side face (fixed x).
  const front = (x1: number, x2: number, z1: number, z2: number, y: number, fill: string, cls?: string, style?: CSSProperties) =>
    poly([P(x1, y, z1), P(x2, y, z1), P(x2, y, z2), P(x1, y, z2)], fill, cls, style)
  const side = (y1: number, y2: number, z1: number, z2: number, x: number, fill: string, cls?: string, style?: CSSProperties) =>
    poly([P(x, y1, z1), P(x, y2, z1), P(x, y2, z2), P(x, y1, z2)], fill, cls, style)

  const SHELL: Faces = [url('sTop'), url('sFront'), url('sSide')]
  const DARK: Faces = [url('dTop'), url('dFront'), url('dSide')]
  const ANT: Faces = ['#F28B82', '#EA4335', '#B3261E']

  const lift = mode === 'typing' ? -6 : mode === 'thinking' ? -2 : 0
  const headStyle: CSSProperties = { transform: `translateY(${lift}px)` }
  const part = (polys: Poly[], o?: Partial<Part>): Part => ({
    outer: 'robot-drop',
    inner: 'robot-follow',
    polys,
    ...o,
  })

  const arm = (x: number) => [
    ...cube(x, 4.9, 7.0, 1.4, 1.2, 1.0, DARK),
    ...cube(x, 4.6, 5.5, 1.4, 1.8, 1.6, SHELL),
    ...cube(x + 0.1, 4.7, 4.85, 1.2, 1.6, 0.65, DARK),
    ...cube(x, 4.6, 3.3, 1.4, 1.8, 1.55, SHELL),
    ...cube(x + 0.15, 4.75, 2.65, 1.1, 1.5, 0.65, DARK),
  ]
  const legs = [
    ...cube(4.45, 4.9, 0.9, 1.6, 2, 1.2, DARK),
    ...cube(3.9, 4.3, 0, 2.7, 3.4, 0.9, SHELL),
    ...cube(7.95, 4.9, 0.9, 1.6, 2, 1.2, DARK),
    ...cube(7.4, 4.3, 0, 2.7, 3.4, 0.9, SHELL),
  ]

  // Body: a shell box with a circuit board on the chest.
  const body = cube(3.5, 3.5, 2.1, 7, 4, 6, SHELL)
  body.push(front(3.5, 10.5, 7.32, 7.42, 7.5, 'rgba(12,18,32,.10)'))
  body.push(front(4.4, 9.6, 3.0, 6.8, 7.5, url('pcb')))
  body.push(front(5.0, 6.9, 3.6, 5.5, 7.5, '#14171B'))
  body.push(front(5.25, 6.65, 3.85, 5.25, 7.5, '#262B32'))
  for (let i = 0; i < 7; i++) body.push(front(4.9 + i * 0.62, 5.25 + i * 0.62, 6.15, 6.5, 7.5, '#E0AE4A'))
  body.push(front(7.4, 7.9, 3.4, 3.7, 7.5, '#D7DCE3'))
  body.push(front(7.4, 7.9, 4.0, 4.3, 7.5, '#D7DCE3'))
  body.push(front(8.2, 8.9, 5.0, 5.6, 7.5, '#EA4335', 'robot-led'))
  body.push(front(8.2, 8.9, 4.0, 4.6, 7.5, '#34A853', 'robot-led2'))
  body.push(front(8.2, 9.2, 3.2, 3.5, 7.5, '#0F1215'))
  const screws: [x: number, z: number][] = [[4.55, 3.15], [9.3, 3.15], [4.55, 6.5], [9.3, 6.5]]
  for (const [x, z] of screws) {
    body.push(front(x, x + 0.18, z, z + 0.18, 7.5, '#B4BDC9'))
  }
  for (let i = 0; i < 4; i++) body.push(side(4.4, 6.6, 4.0 + i * 0.5, 4.2 + i * 0.5, 10.5, 'rgba(12,18,32,.28)'))

  // Head: shell box, visor with a glare, side vents and a lit ear.
  let head = cube(3, 3, 8.7, 8, 4.6, 5, SHELL)
  head.push(front(3.7, 10.3, 9.4, 13.0, 7.6, url('visor')))
  head.push(poly([P(4.2, 7.6, 12.8), P(5.6, 7.6, 12.8), P(4.6, 7.6, 9.6), P(3.9, 7.6, 9.6)], 'rgba(255,255,255,.09)'))
  for (let i = 0; i < 3; i++) head.push(side(6.7 + i * 0.28, 6.85 + i * 0.28, 9.6, 12.6, 11, 'rgba(12,18,32,.3)'))
  head = head.concat(cube(11, 4.0, 10.0, 0.6, 2.2, 2.2, DARK))
  head.push(side(4.5, 5.7, 10.5, 11.7, 11.6, '#4285F4', 'robot-led2'))

  // Face: two blue eyes, or four pixels cycling blue, red, green, blue while
  // thinking (spec §2, "Thinking colours").
  let face: Poly[]
  if (mode === 'thinking') {
    face = [
      front(4.6, 5.7, 10.7, 11.8, 7.6, '#4285F4', 'robot-think', { animationDelay: '0s' }),
      front(6.0, 7.1, 10.7, 11.8, 7.6, '#EA4335', 'robot-think', { animationDelay: '.15s' }),
      front(7.4, 8.5, 10.7, 11.8, 7.6, '#34A853', 'robot-think', { animationDelay: '.3s' }),
      front(8.8, 9.9, 10.7, 11.8, 7.6, '#4285F4', 'robot-think', { animationDelay: '.45s' }),
    ]
  } else {
    // Squint while listening.
    const eh = mode === 'typing' ? 1.1 : 1.8
    face = [
      front(5.2, 6.6, 10.4, 10.4 + eh, 7.6, '#4285F4', 'robot-blink robot-glow'),
      front(7.4, 8.8, 10.4, 10.4 + eh, 7.6, '#4285F4', 'robot-blink robot-glow'),
    ]
  }
  // Eyes move up to ±6px across and ±4px up and down (spec §7, cursor follow).
  const eyeStyle: CSSProperties = {
    transform: `translate(${(lx * 6).toFixed(1)}px, ${(ly * 4 + lift).toFixed(1)}px)`,
  }

  const partsA = [
    part(arm(2.1), { outerStyle: { animationDelay: '.9s' } }),
    part(legs, { outerStyle: { animationDelay: '.2s' }, inner: '' }),
    part(body, { outerStyle: { animationDelay: '.5s' } }),
    part(cube(6, 5, 8.1, 2, 1, 0.6, DARK), { outerStyle: { animationDelay: '1.2s' }, innerStyle: headStyle }),
    part(head, { outerStyle: { animationDelay: '1.2s' }, innerStyle: headStyle }),
    part(face, { outerStyle: { animationDelay: '1.2s' }, innerStyle: eyeStyle }),
  ]
  const partsB = [
    part(cube(9.4, 4, 13.7, 0.4, 0.4, 2.4, DARK).concat(cube(9.1, 3.7, 16.1, 1, 1, 1, ANT, 'robot-ant')), {
      outerStyle: { animationDelay: '1.7s' },
      innerStyle: headStyle,
    }),
    part(arm(10.5), {
      outerStyle: { animationDelay: '.9s' },
      inner: 'robot-wave',
      innerStyle: { transformOrigin: '64.2px 4.6px' },
    }),
  ]
  return { partsA, partsB, headStyle }
}
