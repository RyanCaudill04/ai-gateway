import { useState } from 'react'
import { AreaChart } from '../../components/ui/AreaChart.tsx'
import { SegmentedControl } from '../../components/ui/SegmentedControl.tsx'
import { RANGES, SERIES, type Range } from './sample.ts'

const RANGE_OPTIONS = RANGES.map((r) => ({ value: r, label: r }))

/** The request total and chart for the picked range (24h, 7d or 30d). */
export function RequestsWell() {
  const [range, setRange] = useState<Range>('7d')
  const series = SERIES[range]

  return (
    <section className="well keys-requests" aria-label="Requests over time">
      <div className="keys-requests-top">
        <div className="keys-requests-total">
          <span className="keys-requests-label">Requests</span>
          <span className="big-number keys-requests-number">{series.total}</span>
        </div>
        {/* Sits on a well, so the track uses well2 to stand out (see KeysScreen.css). */}
        <div className="keys-range">
          <SegmentedControl
            label="Range"
            options={RANGE_OPTIONS}
            value={range}
            onChange={setRange}
            size="sm"
          />
        </div>
      </div>
      <AreaChart points={series.points} label={`Requests over ${series.label}`} />
      <div className="keys-axis">
        <span>{series.start}</span>
        <span>now</span>
      </div>
    </section>
  )
}
