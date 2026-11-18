import { useState } from 'react'
import { ScreenLayout } from '../components/shell/ScreenLayout.tsx'
import { CardHeader } from '../components/ui/CardHeader.tsx'
import { Chip } from '../components/ui/Chip.tsx'
import { REQUESTS } from '../sample.ts'
import { RequestList } from './routing/RequestList.tsx'
import { RoutingInspector } from './routing/RoutingInspector.tsx'
import { RoutingStage } from './routing/RoutingStage.tsx'
import { CLASSIFIER_VERSION } from './routing/sample.ts'
import './RoutingScreen.css'

/**
 * Routing: replays how the classifier routed a recent request (spec §8).
 * TODO(api): REQUESTS is sample data until the router reports its decisions.
 */
export function RoutingScreen() {
  // `replay` goes up on every pick, including the row that's already
  // selected, so clicking a request always plays its path again.
  const [picked, setPicked] = useState({ id: REQUESTS[0].id, replay: 0 })
  const request = REQUESTS.find((r) => r.id === picked.id) ?? REQUESTS[0]
  const swapKey = `${picked.id}-${picked.replay}`

  function pick(id: string) {
    setPicked((p) => ({ id, replay: p.replay + 1 }))
  }

  const stats = [
    { label: 'Served by', value: request.model },
    { label: 'Provider', value: request.provider },
    { label: 'Total latency', value: `${request.ms} ms` },
  ]

  return (
    <ScreenLayout
      sideLabel="Recent requests"
      side={<RequestList requests={REQUESTS} selectedId={picked.id} onPick={pick} />}
      main={
        <>
          <CardHeader title="Routing">
            <Chip mono>classifier {CLASSIFIER_VERSION}</Chip>
          </CardHeader>
          <RoutingStage request={request} swapKey={swapKey} />
          <section className="routing-stats" aria-label="Result">
            {stats.map((s) => (
              <div key={s.label} className="well routing-stat">
                <span className="routing-stat-label">{s.label}</span>
                <span key={swapKey} className="routing-stat-value routing-swap">
                  {s.value}
                </span>
              </div>
            ))}
          </section>
        </>
      }
      inspectorLabel="Details"
      inspector={<RoutingInspector signals={request.signals} swapKey={swapKey} />}
    />
  )
}
