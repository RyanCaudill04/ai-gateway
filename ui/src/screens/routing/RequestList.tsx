import { SideListHeader, SideRow } from '../../components/ui/SideList.tsx'
import { TierDot } from '../../components/ui/Dot.tsx'
import type { RoutedRequest } from '../../sample.ts'

interface RequestListProps {
  requests: RoutedRequest[]
  selectedId: string
  /** Called on every click, even on the selected row, so it can replay. */
  onPick: (id: string) => void
}

/** The side list of recent requests. Picking one replays its path on the stage. */
export function RequestList({ requests, selectedId, onPick }: RequestListProps) {
  return (
    <>
      <SideListHeader title="Requests">
        {/* TODO(api): only "live" once this list actually streams in. */}
        <span className="chip routing-live">
          <span className="routing-live-dot" aria-hidden="true" />
          live
        </span>
      </SideListHeader>
      {requests.map((r) => (
        <SideRow
          key={r.id}
          title={r.prompt}
          meta={
            <>
              <TierDot tier={r.tier} />
              {r.tier} · {r.ms} ms
            </>
          }
          selected={r.id === selectedId}
          onClick={() => onPick(r.id)}
        />
      ))}
    </>
  )
}
