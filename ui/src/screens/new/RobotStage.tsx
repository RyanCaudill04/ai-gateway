import { useState, type MouseEvent } from 'react'
import { Robot, type RobotMode } from '../../components/robot/Robot.tsx'
import { Dot } from '../../components/ui/Dot.tsx'
import type { DotColor } from '../../components/ui/colors.ts'

const STATUS: Record<RobotMode, { text: string; dot: DotColor }> = {
  idle: { text: 'Idle', dot: 'muted' },
  typing: { text: 'Listening', dot: 'blue' },
  thinking: { text: 'Thinking', dot: 'green' },
}

/** -1..1 from where the pointer is across a length. */
function toUnit(offset: number, length: number): number {
  return Math.max(-1, Math.min(1, (offset / length - 0.5) * 2))
}

/**
 * The robot on its stage, with a status chip top-left and a hint bottom-left
 * (spec §8, Landing). The eyes follow the pointer while it's over the stage.
 */
export function RobotStage({ mode }: { mode: RobotMode }) {
  // Kept here rather than in the screen so a mouse move only re-renders the
  // stage.
  const [look, setLook] = useState({ lx: 0, ly: 0 })
  const status = STATUS[mode]

  function handleMove(e: MouseEvent<HTMLElement>) {
    const box = e.currentTarget.getBoundingClientRect()
    setLook({
      lx: toUnit(e.clientX - box.left, box.width),
      ly: toUnit(e.clientY - box.top, box.height),
    })
  }

  return (
    <section
      className="new-stage"
      aria-label="Robot"
      onMouseMove={handleMove}
      onMouseLeave={() => setLook({ lx: 0, ly: 0 })}
    >
      <div className="new-stage-status">
        <Dot color={status.dot} size={8} pulse={mode !== 'idle'} />
        {status.text}
      </div>
      <Robot mode={mode} lx={look.lx} ly={look.ly} className="new-stage-robot" />
      <p className="new-stage-hint">Move your cursor. Start typing. Hit send.</p>
    </section>
  )
}
