import { useEffect, useRef, useState } from 'react'
import { Composer } from '../components/Composer.tsx'
import type { RobotMode } from '../components/robot/Robot.tsx'
import { ScreenLayout } from '../components/shell/ScreenLayout.tsx'
import { TierPicker } from '../components/TierPicker.tsx'
import { CardHeader } from '../components/ui/CardHeader.tsx'
import { Chip } from '../components/ui/Chip.tsx'
import { TierDot } from '../components/ui/Dot.tsx'
import { SideListGroup, SideListHeader, SideRow } from '../components/ui/SideList.tsx'
import { CHATS, ENDPOINT, type TierChoice } from '../sample.ts'
import { screenHref } from '../screens.ts'
import { NewInspector } from './new/NewInspector.tsx'
import { RobotStage } from './new/RobotStage.tsx'
import './NewScreen.css'

// How long the robot keeps "listening" after the last keystroke.
const TYPING_MS = 900
// TODO(api): think until the response arrives, not for a fixed time. 4.2s is
// the mockup's stand-in.
const THINKING_MS = 4200

/** The landing page: the robot, the big composer and the tier picker (spec §8). */
export function NewScreen() {
  const [tier, setTier] = useState<TierChoice>('auto')
  const [typing, setTyping] = useState(false)
  const [thinking, setThinking] = useState(false)
  const typingTimer = useRef<number | undefined>(undefined)
  const thinkingTimer = useRef<number | undefined>(undefined)

  // Don't let a timer fire setState after the screen is gone.
  useEffect(
    () => () => {
      clearTimeout(typingTimer.current)
      clearTimeout(thinkingTimer.current)
    },
    [],
  )

  const mode: RobotMode = thinking ? 'thinking' : typing ? 'typing' : 'idle'

  function handleInput() {
    setTyping(true)
    clearTimeout(typingTimer.current)
    typingTimer.current = window.setTimeout(() => setTyping(false), TYPING_MS)
  }

  // Composer doesn't call this yet (its send is still to write); once it does,
  // the robot thinks. TODO(api): send the text and tier to the router.
  function handleSend() {
    clearTimeout(thinkingTimer.current)
    setTyping(false)
    setThinking(true)
    thinkingTimer.current = window.setTimeout(() => setThinking(false), THINKING_MS)
  }

  return (
    <ScreenLayout
      sideLabel="Conversations"
      side={
        <>
          <SideListHeader title="Chats">
            <span className="chip mono new-shortcut">⌘K</span>
          </SideListHeader>
          <SideListGroup label="Today" />
          {CHATS.map((chat) => (
            <SideRow
              key={chat.id}
              href={screenHref('chats')}
              title={chat.title}
              meta={
                <>
                  <TierDot tier={chat.tier} />
                  {chat.tier} · {chat.turns} turns
                </>
              }
            />
          ))}
        </>
      }
      main={
        <>
          <CardHeader title="New conversation">
            <Chip mono>{ENDPOINT}</Chip>
          </CardHeader>
          <RobotStage mode={mode} />
          <section className="new-message" aria-label="New message">
            <h2 className="new-headline">What are we working on?</h2>
            <Composer
              variant="hero"
              placeholder="Describe a task, paste code, or ask a question…"
              onInput={handleInput}
              onSend={handleSend}
            />
            <TierPicker value={tier} onChange={setTier} />
          </section>
        </>
      }
      inspectorLabel="Overview"
      inspector={<NewInspector />}
    />
  )
}
