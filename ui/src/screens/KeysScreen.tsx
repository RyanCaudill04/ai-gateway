import { useState } from 'react'
import { CardHeader } from '../components/ui/CardHeader.tsx'
import { PillButton } from '../components/ui/PillButton.tsx'
import { ScreenLayout } from '../components/shell/ScreenLayout.tsx'
import { KeyList } from './keys/KeyList.tsx'
import { KeyWell } from './keys/KeyWell.tsx'
import { Headroom, KeyFacts } from './keys/Limits.tsx'
import { RecentRequests } from './keys/RecentRequests.tsx'
import { RequestsWell } from './keys/RequestsWell.tsx'
import { KEYS, RECENT } from './keys/sample.ts'
import './KeysScreen.css'

/**
 * Keys & usage (spec §8): the API keys on the left, the picked key's usage in
 * the middle, and free-tier headroom on the right. All sample data for now.
 */
export function KeysScreen() {
  const [selectedId, setSelectedId] = useState(KEYS[0].id)
  const current = KEYS.find((k) => k.id === selectedId) ?? KEYS[0]

  return (
    <ScreenLayout
      sideLabel="API keys"
      side={<KeyList keys={KEYS} selectedId={current.id} onSelect={setSelectedId} />}
      main={
        <>
          <CardHeader title={current.name}>
            {/* TODO(api): confirm, then revoke the key. */}
            <PillButton tone="danger" onClick={() => {}}>
              Revoke key
            </PillButton>
          </CardHeader>
          <div className="keys-main">
            {/* Keyed by key so a new pick starts with the key hidden again. */}
            <KeyWell key={`key-${current.id}`} apiKey={current} />
            <RequestsWell />
            {/* Keyed too, so the rows rise in again for the new key. Sibling
                keys must differ, hence the prefixes. */}
            <RecentRequests key={`recent-${current.id}`} requests={RECENT} />
          </div>
        </>
      }
      inspectorLabel="Limits"
      inspector={
        <>
          <Headroom />
          <KeyFacts apiKey={current} />
        </>
      }
    />
  )
}
