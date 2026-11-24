import { AUTO_HINT, TIER_ORDER, TIERS, type TierChoice } from '../sample.ts'
import { RingDot, TierDot } from './ui/Dot.tsx'
import { SegmentedControl, type Segment } from './ui/SegmentedControl.tsx'
import './TierPicker.css'

const OPTIONS: Segment<TierChoice>[] = [
  { value: 'auto', label: 'Auto', icon: <RingDot /> },
  ...TIER_ORDER.map((tier) => ({
    value: tier,
    label: TIERS[tier].label,
    icon: <TierDot tier={tier} size={8} />,
  })),
]

interface TierPickerProps {
  value: TierChoice
  onChange: (value: TierChoice) => void
}

/** Auto / Fast / Balanced / Deep, with a one-line hint for the one picked. */
export function TierPicker({ value, onChange }: TierPickerProps) {
  return (
    <div className="tier-picker">
      <SegmentedControl label="Model tier" options={OPTIONS} value={value} onChange={onChange} />
      <span className="tier-hint">{value === 'auto' ? AUTO_HINT : TIERS[value].hint}</span>
    </div>
  )
}
