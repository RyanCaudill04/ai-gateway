import './AreaChart.css'

// The chart is drawn in a fixed 1000 x 170 box and stretched to fit its
// container (design/source/Keys.dc.html). The line keeps a 3px stroke however
// wide it gets because of vector-effect="non-scaling-stroke".
const WIDTH = 1000
const HEIGHT = 170
// The tallest point sits 8px below the top so the 3px line isn't clipped, and
// the lowest possible one 8px above the bottom so the area never thins out.
const BOTTOM = 162
const RANGE = 148

interface AreaChartProps {
  /** One value per time step, oldest first. Only the shape matters: the tallest fills the height. */
  points: number[]
  /** Names the chart for screen readers, e.g. "Requests over the last 7 days". */
  label: string
}

/**
 * A smooth line through the points. Each segment is a cubic whose control
 * points sit halfway across at the start and end heights, so the curve is
 * flat at every point and never overshoots it.
 */
function linePath(points: number[]): string {
  // A single point still needs two ends to make a line.
  const values = points.length === 1 ? [points[0], points[0]] : points
  const max = Math.max(...values) || 1
  const step = WIDTH / (values.length - 1)
  const xy = values.map((v, i) => [i * step, BOTTOM - (v / max) * RANGE])

  let d = `M${xy[0][0].toFixed(1)} ${xy[0][1].toFixed(1)}`
  for (let i = 1; i < xy.length; i++) {
    const [x0, y0] = xy[i - 1]
    const [x1, y1] = xy[i]
    const mx = ((x0 + x1) / 2).toFixed(1)
    d += ` C${mx} ${y0.toFixed(1)} ${mx} ${y1.toFixed(1)} ${x1.toFixed(1)} ${y1.toFixed(1)}`
  }
  return d
}

/** A blue line over a light blue area that draws itself in (spec §6, "Chart"). */
export function AreaChart({ points, label }: AreaChartProps) {
  if (points.length === 0) return null
  const line = linePath(points)
  // The area is the line closed off along the bottom edge.
  const area = `${line} L${WIDTH} ${HEIGHT} L0 ${HEIGHT} Z`

  return (
    <svg
      // New points get a new key, which remounts the paths so the draw-in
      // plays again (e.g. on a range change) instead of the line jumping.
      key={points.join(',')}
      className="area-chart"
      viewBox={`0 0 ${WIDTH} ${HEIGHT}`}
      preserveAspectRatio="none"
      role="img"
      aria-label={label}
    >
      <path className="area-chart-area" d={area} />
      <path className="area-chart-line" d={line} vectorEffect="non-scaling-stroke" />
    </svg>
  )
}
