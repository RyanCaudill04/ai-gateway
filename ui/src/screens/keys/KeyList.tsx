import { Icon } from '../../components/Icon.tsx'
import { Chip } from '../../components/ui/Chip.tsx'
import { Dot } from '../../components/ui/Dot.tsx'
import { PillButton } from '../../components/ui/PillButton.tsx'
import { SideListHeader, SideRow } from '../../components/ui/SideList.tsx'
import type { ApiKey } from './sample.ts'

interface KeyListProps {
  keys: ApiKey[]
  selectedId: string
  onSelect: (id: string) => void
}

/** The side list: every API key, then the Create key button pinned to the bottom. */
export function KeyList({ keys, selectedId, onSelect }: KeyListProps) {
  return (
    <>
      <SideListHeader title="Keys">
        <Chip mono>{keys.length} active</Chip>
      </SideListHeader>
      {keys.map((k) => (
        <SideRow
          key={k.id}
          title={
            <span className="keys-row-name">
              <Dot color="green" pulse />
              {k.name}
            </span>
          }
          meta={`used ${k.lastUsed}`}
          selected={k.id === selectedId}
          onClick={() => onSelect(k.id)}
        />
      ))}
      {/* TODO(api): open a create-key flow once the router can issue keys. */}
      <PillButton tone="primary" className="keys-create" onClick={() => {}}>
        <Icon name="plus" />
        Create key
      </PillButton>
    </>
  )
}
